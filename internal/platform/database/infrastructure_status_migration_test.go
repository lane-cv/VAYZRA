package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestInfrastructureStatusMigrationAllowlistAndSecretFreeShape(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema='public'
  AND table_name='infrastructure_configuration_status'
ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if len(columns) != 3 || columns[0] != "configuration_key" ||
		columns[1] != "configured" || columns[2] != "last_validated_at" {
		t.Fatalf("columns=%v", columns)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO infrastructure_configuration_status(
  configuration_key,configured,last_validated_at
) VALUES('database_url',true,clock_timestamp())`); err == nil {
		t.Fatal("non-allowlisted configuration key was accepted")
	}
}
