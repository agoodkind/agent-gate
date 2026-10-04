package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

const databaseClientRulesPath = "../../examples/rules/no-direct-database-clients.toml"

func loadDatabaseClientConfig(t *testing.T) *config.Config {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(databaseClientRulesPath))
	if err != nil {
		t.Fatalf("read database client rules: %v", err)
	}
	cfg := loadRuleConfig(t, string(body))
	validationErrors := hook.ValidateConfig(cfg)
	if len(validationErrors) != 0 {
		t.Fatalf("database client rules are invalid for shipped hook schemas: %v", validationErrors)
	}
	return cfg
}

func TestDatabaseClientRulesBlockDirectSQL(t *testing.T) {
	cfg := loadDatabaseClientConfig(t)
	blocked := []struct {
		name    string
		command string
		rule    string
	}{
		{"ysqlsh", "ysqlsh -h yb1 -U yugabyte -d tack", "no-database-client-ysqlsh"},
		{"ysqlsh with a password prefix", "PGPASSWORD=canary ysqlsh -c 'select 1'", "no-database-client-ysqlsh"},
		{"psql", "psql postgres://canary@localhost/tack", "no-database-client-psql"},
		{"psql after cd", "cd /tmp && psql -c 'select 1'", "no-database-client-psql"},
		{"docker exec ysqlsh", "docker exec tack-yugabyte-1 ysqlsh -c 'select 1'", "no-database-client-wrapper"},
		{"docker exec ysqlsh by path", "docker exec -it tack-yugabyte-1 /home/yugabyte/bin/ysqlsh", "no-database-client-wrapper"},
		{"docker compose exec psql", "docker compose exec yugabyte psql -U yugabyte", "no-database-client-wrapper"},
		{"ssh docker exec ysqlsh", `ssh root@canary-host 'docker exec tack-yugabyte-1 ysqlsh -c "select 1"'`, "no-database-client-wrapper"},
		{"ssh psql", "ssh canary-host psql -c 'select 1'", "no-database-client-wrapper"},
		{"bash script runs ysqlsh", "bash -c 'ysqlsh -c \"select 1\"'", "no-database-client-wrapper"},
		{"break-glass execute", `./server ops db sql --statement "select 1" --reason canary --execute`, "no-database-break-glass-execute"},
		{"break-glass execute in compose", `docker compose run --rm tack-ops ops db sql --execute --statement "select 1" --reason canary`, "no-database-break-glass-execute"},
		{"break-glass execute over ssh", `ssh canary-host 'cd /root/tack && docker compose run --rm tack-ops ops db sql --statement "select 1" --reason canary --execute'`, "no-database-break-glass-execute"},
		{"ssh create table", `ssh canary-host '/root/qa_sql.sh "create table audit.events_canary partition of audit.events for values from (1) to (2)"'`, "no-remote-sql-statement"},
		{"ssh alter table", `ssh canary-host "./run.sh 'ALTER TABLE users ADD COLUMN canary int'"`, "no-remote-sql-statement"},
		{"ssh drop table", `ssh canary-host "./run.sh 'DROP TABLE canary'"`, "no-remote-sql-statement"},
		{"ssh insert", `ssh canary-host "./run.sh 'insert into users values (1)'"`, "no-remote-sql-statement"},
		{"ssh update", `ssh canary-host "./run.sh 'update users set name = 1'"`, "no-remote-sql-statement"},
		{"ssh delete", `ssh canary-host "./run.sh 'delete from users'"`, "no-remote-sql-statement"},
		{"ssh truncate", `ssh canary-host "./run.sh 'truncate table users'"`, "no-remote-sql-statement"},
		{"ssh grant", `ssh canary-host "./run.sh 'grant select on users to canary'"`, "no-remote-sql-statement"},
	}
	for _, testCase := range blocked {
		t.Run(testCase.name, func(t *testing.T) {
			matched := map[string]string{}
			for _, violation := range secretDumpViolations(t, cfg, testCase.command) {
				matched[violation.RuleName] = violation.Message
			}
			message, ok := matched[testCase.rule]
			if !ok {
				t.Fatalf("command %q did not match %s; matched %v", testCase.command, testCase.rule, matched)
			}
			if !strings.Contains(message, "./server ops <command>") {
				t.Fatalf("message of %s does not state the reviewed command path: %q", testCase.rule, message)
			}
		})
	}
}

func TestDatabaseClientRulesAllowReviewedCommands(t *testing.T) {
	cfg := loadDatabaseClientConfig(t)
	allowed := []struct {
		name    string
		command string
	}{
		{"ops search verify", "./server ops search verify"},
		{"ops command over ssh", "ssh canary-host 'cd /root/tack && docker compose run --rm tack-ops ops deploy verify'"},
		{"ops db sql dry run", `./server ops db sql --statement "select 1" --reason canary`},
		{"migrate in compose", "docker compose run --rm tack-ops migrate"},
		{"ssh reads docker state", "ssh canary-host 'docker ps --format \"{{.Names}}\"'"},
		{"ssh updates packages", "ssh canary-host 'apt-get update'"},
		{"docker exec fdbcli", "docker exec tack-fdb-1 fdbcli --exec 'status minimal'"},
		{"commit message quotes the clients", `git commit -m "block ysqlsh, psql, and ops db sql --execute"`},
		{"echo quotes the break-glass command", `echo "ops db sql --execute"`},
		{"go test of a psql named package", "go test ./internal/psqlutil/..."},
		{"patch body lists the clients", "*** Begin Patch\n*** Add File: canary.sh\n+ysqlsh\n+psql\n*** End Patch"},
		{"heredoc body mentions the clients", "git commit -F - <<'EOF'\nysqlsh\npsql -c 'drop table canary'\nEOF"},
	}
	for _, testCase := range allowed {
		t.Run(testCase.name, func(t *testing.T) {
			violations := secretDumpViolations(t, cfg, testCase.command)
			if len(violations) != 0 {
				t.Fatalf("command %q was blocked by rule %q: %s",
					testCase.command, violations[0].RuleName, violations[0].Message)
			}
		})
	}
}
