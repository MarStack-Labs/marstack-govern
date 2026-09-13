package cost

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvoiceNotFound = errors.New("no invoice has been generated for that period")

func knownWorkload(raw string) *string {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}

	value := parsed.String()

	return &value
}

func (s *Store) SaveInvoice(ctx context.Context, invoice Invoice) error {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var divisionUID string
	err = transaction.QueryRow(ctx,
		`SELECT uid FROM divisions WHERE name = $1`, invoice.Division).Scan(&divisionUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("division %s has not been projected yet", invoice.Division)
	}
	if err != nil {
		return fmt.Errorf("find division %s: %w", invoice.Division, err)
	}

	invoiceUID := uuid.NewString()

	err = transaction.QueryRow(ctx, `
		INSERT INTO invoices (
		    uid, division_uid, period_start, period_end,
		    pricing_policy_name, pricing_policy_revision, currency,
		    subtotal, unallocated_share, total, inputs_digest, generated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (division_uid, period_start, period_end) DO UPDATE SET
		    pricing_policy_name = excluded.pricing_policy_name,
		    pricing_policy_revision = excluded.pricing_policy_revision,
		    currency = excluded.currency,
		    subtotal = excluded.subtotal,
		    unallocated_share = excluded.unallocated_share,
		    total = excluded.total,
		    inputs_digest = excluded.inputs_digest,
		    generated_at = excluded.generated_at
		RETURNING uid`,
		invoiceUID, divisionUID, invoice.PeriodStart, invoice.PeriodEnd,
		invoice.PolicyName, invoice.PolicyRevision, invoice.Currency,
		invoice.Subtotal.String(), invoice.UnallocatedShare.String(), invoice.Total.String(),
		invoice.InputsDigest, invoice.GeneratedAt,
	).Scan(&invoiceUID)
	if err != nil {
		return fmt.Errorf("save invoice for %s: %w", invoice.Division, err)
	}

	if _, err := transaction.Exec(ctx,
		`DELETE FROM invoice_lines WHERE invoice_uid = $1`, invoiceUID); err != nil {
		return fmt.Errorf("clear invoice lines: %w", err)
	}

	for _, line := range invoice.Lines {
		workloadUID := knownWorkload(line.WorkloadUID)

		_, err := transaction.Exec(ctx, `
			INSERT INTO invoice_lines (
			    invoice_uid, line_no, workload_uid, workload_label,
			    resource, quantity, unit, rate, amount
			) VALUES ($1, $2, (SELECT uid FROM workloads WHERE uid = $3), $4, $5, $6, $7, $8, $9)`,
			invoiceUID, line.Number, workloadUID, line.WorkloadLabel,
			string(line.Resource), line.Quantity.FloatString(6), line.Unit,
			line.Rate.String(), line.Amount.String(),
		)
		if err != nil {
			return fmt.Errorf("save line %d: %w", line.Number, err)
		}
	}

	return transaction.Commit(ctx)
}

func (s *Store) InvoiceByUID(ctx context.Context, uid string) (Invoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.uid, d.name, i.period_start, i.period_end,
		       i.pricing_policy_name, i.pricing_policy_revision, i.currency,
		       i.subtotal, i.unallocated_share, i.total, i.inputs_digest, i.generated_at
		  FROM invoices i
		  JOIN divisions d ON d.uid = i.division_uid
		 WHERE i.uid = $1`, uid)
	if err != nil {
		return Invoice{}, fmt.Errorf("read invoice: %w", err)
	}

	invoices, err := scanInvoices(ctx, s, rows)
	if err != nil {
		return Invoice{}, err
	}
	if len(invoices) == 0 {
		return Invoice{}, fmt.Errorf("invoice %s: %w", uid, ErrInvoiceNotFound)
	}

	return invoices[0], nil
}

func (s *Store) Invoices(ctx context.Context, division string, limit int) ([]Invoice, error) {
	if limit <= 0 || limit > 60 {
		limit = 24
	}

	rows, err := s.pool.Query(ctx, `
		SELECT i.uid, d.name, i.period_start, i.period_end,
		       i.pricing_policy_name, i.pricing_policy_revision, i.currency,
		       i.subtotal, i.unallocated_share, i.total, i.inputs_digest, i.generated_at
		  FROM invoices i
		  JOIN divisions d ON d.uid = i.division_uid
		 WHERE d.name = $1
		 ORDER BY i.period_start DESC
		 LIMIT $2`, division, limit)
	if err != nil {
		return nil, fmt.Errorf("list invoices: %w", err)
	}

	return scanInvoices(ctx, s, rows)
}

func scanInvoices(ctx context.Context, s *Store, rows pgx.Rows) ([]Invoice, error) {
	defer rows.Close()

	type header struct {
		uid     string
		invoice Invoice
	}

	headers := []header{}

	for rows.Next() {
		var (
			subtotal, unallocated, total string
			invoice                      Invoice
		)

		if err := rows.Scan(
			&invoice.UID, &invoice.Division, &invoice.PeriodStart, &invoice.PeriodEnd,
			&invoice.PolicyName, &invoice.PolicyRevision, &invoice.Currency,
			&subtotal, &unallocated, &total, &invoice.InputsDigest, &invoice.GeneratedAt,
		); err != nil {
			return nil, fmt.Errorf("scan invoice: %w", err)
		}

		for _, pair := range []struct {
			raw    string
			target *Money
		}{
			{subtotal, &invoice.Subtotal},
			{unallocated, &invoice.UnallocatedShare},
			{total, &invoice.Total},
		} {
			parsed, err := ParseMoney(invoice.Currency, pair.raw)
			if err != nil {
				return nil, fmt.Errorf("stored amount %q is unreadable: %w", pair.raw, err)
			}
			*pair.target = parsed
		}

		headers = append(headers, header{uid: invoice.UID, invoice: invoice})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read invoices: %w", err)
	}

	out := make([]Invoice, 0, len(headers))
	for _, found := range headers {
		lines, err := s.lines(ctx, found.uid, found.invoice.Currency)
		if err != nil {
			return nil, err
		}

		found.invoice.Lines = lines
		out = append(out, found.invoice)
	}

	return out, nil
}

func (s *Store) lines(ctx context.Context, invoiceUID, currency string) ([]InvoiceItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT line_no, coalesce(workload_uid::text, ''), workload_label,
		       resource, quantity, unit, rate, amount
		  FROM invoice_lines
		 WHERE invoice_uid = $1
		 ORDER BY line_no`, invoiceUID)
	if err != nil {
		return nil, fmt.Errorf("read invoice lines: %w", err)
	}
	defer rows.Close()

	lines := []InvoiceItem{}

	for rows.Next() {
		var (
			line                InvoiceItem
			resource            string
			quantity, rate, sum string
		)

		if err := rows.Scan(&line.Number, &line.WorkloadUID, &line.WorkloadLabel,
			&resource, &quantity, &line.Unit, &rate, &sum); err != nil {
			return nil, fmt.Errorf("scan invoice line: %w", err)
		}

		line.Resource = Resource(resource)

		parsed, ok := new(big.Rat).SetString(quantity)
		if !ok {
			return nil, fmt.Errorf("stored quantity %q is unreadable", quantity)
		}
		line.Quantity = parsed

		for _, pair := range []struct {
			raw    string
			target *Money
		}{{rate, &line.Rate}, {sum, &line.Amount}} {
			amount, err := ParseMoney(currency, pair.raw)
			if err != nil {
				return nil, fmt.Errorf("stored amount %q is unreadable: %w", pair.raw, err)
			}
			*pair.target = amount
		}

		lines = append(lines, line)
	}

	return lines, rows.Err()
}
