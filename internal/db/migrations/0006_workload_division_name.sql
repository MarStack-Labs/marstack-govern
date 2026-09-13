ALTER TABLE workloads ADD COLUMN division_name text;

CREATE INDEX workloads_division_name_idx ON workloads (division_name);
