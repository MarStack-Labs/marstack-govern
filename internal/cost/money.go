package cost

import (
	"fmt"
	"math/big"
	"strings"
)

type Money struct {
	currency string
	amount   *big.Rat
}

func NewMoney(currency string, amount *big.Rat) Money {
	if currency == "" {
		currency = "IDR"
	}
	if amount == nil {
		amount = new(big.Rat)
	}

	return Money{currency: currency, amount: amount}
}

func ParseMoney(currency, amount string) (Money, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return NewMoney(currency, nil), nil
	}

	parsed, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return Money{}, fmt.Errorf("%q is not an amount", amount)
	}

	return NewMoney(currency, parsed), nil
}

func (m Money) Currency() string {
	if m.currency == "" {
		return "IDR"
	}

	return m.currency
}

func (m Money) Rat() *big.Rat {
	if m.amount == nil {
		return new(big.Rat)
	}

	return new(big.Rat).Set(m.amount)
}

func (m Money) Times(factor *big.Rat) Money {
	return NewMoney(m.Currency(), new(big.Rat).Mul(m.Rat(), factor))
}

func (m Money) Add(other Money) Money {
	return NewMoney(m.Currency(), new(big.Rat).Add(m.Rat(), other.Rat()))
}

func (m Money) Sub(other Money) Money {
	return NewMoney(m.Currency(), new(big.Rat).Sub(m.Rat(), other.Rat()))
}

func (m Money) IsZero() bool {
	return m.Rat().Sign() == 0
}

func (m Money) Negative() bool {
	return m.Rat().Sign() < 0
}

func (m Money) String() string {
	return m.Rat().FloatString(2)
}

func Ratio(numerator, denominator int64) *big.Rat {
	if denominator == 0 {
		return new(big.Rat)
	}

	return big.NewRat(numerator, denominator)
}

func FromFloat(value float64) *big.Rat {
	rat := new(big.Rat).SetFloat64(value)
	if rat == nil {
		return new(big.Rat)
	}

	return rat
}
