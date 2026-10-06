package content

import (
	"context"
	"path/filepath"
	"testing"
)

func aReleasedSchema21Database(t *testing.T, path string) {
	t.Helper()
	rawExec(t, path,
		"PRAGMA auto_vacuum=INCREMENTAL",
		"PRAGMA journal_mode=WAL",
		releasedSchema(t, 21),
		`INSERT INTO workspaces(id,name,created_at) VALUES('ws-21','workspace 21',2100)`,
		`INSERT INTO tabs(id,workspace_id,name) VALUES('tab-21','ws-21','tab 21')`,
		`INSERT INTO panes(id,tab_id,cwd,kind) VALUES('pane-21','tab-21','/workspace','local')`,
		`INSERT INTO environments(id,kind,first_seen) VALUES('env-21','local',2100)`,
		`INSERT INTO environment_observations(id,environment_id,version,observed_at,criticality) VALUES(21,'env-21',1,2100,'routine')`,
		`INSERT INTO entries(id,ingest_seq,client,digest,environment_id,pane_id,cwd,kind,source,intent,phase,status,submitted_at)
		 VALUES('entry-21',21,'client-21','entry-digest-21','env-21','pane-21','/workspace','shell','user','echo durable','closed','success',2100)`,
		`INSERT INTO executions(id,entry_id,environment_obs_id) VALUES(21,'entry-21',21)`,
		`INSERT INTO authority_grants(id,execution_id,version,issued_at,expires_at,policy) VALUES(91,21,7,2101,999999,'{"matrix":"kept"}')`,
		`INSERT INTO grant_scopes(grant_id,resource_kind,resource_id) VALUES(91,'path','/workspace/private')`,
		`INSERT INTO grant_scopes(grant_id,resource_kind,resource_id) VALUES(91,'session','session-21')`,
		`INSERT INTO grant_effects(grant_id,effect) VALUES(91,'observe')`,
		`INSERT INTO grant_effects(grant_id,effect) VALUES(91,'mutate-reversible')`,
		`PRAGMA user_version=21`,
	)
}

func TestSchema21To22PreservesExecutionGrantAndAddsTypedLaunchConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema21Database(t, path)
	openStore(t, path)
	if got := rawUserVersion(t, path); got != 22 {
		t.Fatalf("user_version=%d, want 22", got)
	}
	for _, q := range []struct {
		query string
		want  []string
	}{
		{`SELECT id||'|'||execution_id||'|'||version||'|'||issued_at||'|'||expires_at||'|'||policy FROM authority_grants`, []string{`91|21|7|2101|999999|{"matrix":"kept"}`}},
		{`SELECT resource_kind||'|'||resource_id FROM grant_scopes WHERE grant_id=91 ORDER BY resource_kind`, []string{"path|/workspace/private", "session|session-21"}},
		{`SELECT effect FROM grant_effects WHERE grant_id=91 ORDER BY effect`, []string{"mutate-reversible", "observe"}},
	} {
		if got := rawStrings(t, path, q.query); len(got) != len(q.want) {
			t.Fatalf("%s rows=%v want %v", q.query, got, q.want)
		} else {
			for i := range got {
				if got[i] != q.want[i] {
					t.Fatalf("%s row[%d]=%q want %q", q.query, i, got[i], q.want[i])
				}
			}
		}
	}
	rawExec(t, path, `INSERT INTO pane_launches(id,pane_id,workspace_id,standard_revision,workspace_revision,source_session_id,source_host,source_account,source_generation,mode,state,policy_digest,policy_version,policy,created_at,updated_at) VALUES('launch-21','pane-21','ws-21',0,0,'session-source','host','account','generation','enforce','preparing','digest',1,'{}',1,1)`)
	rawExec(t, path, `INSERT INTO pane_launches(id,pane_id,workspace_id,standard_revision,workspace_revision,source_session_id,source_host,source_account,source_generation,mode,state,created_at,updated_at) VALUES('launch-off-21','pane-21','ws-21',0,0,'session-source','host','account','generation','off','failed',1,1)`)
	if err := rawTryWithForeignKeys(t, path, `INSERT INTO authority_grants(execution_id,launch_id,version,issued_at,expires_at,policy) VALUES(NULL,NULL,1,1,NULL,'{}')`); err == nil {
		t.Fatal("grant with no subject was accepted")
	}
	if err := rawTryWithForeignKeys(t, path, `INSERT INTO authority_grants(execution_id,launch_id,version,issued_at,expires_at,policy) VALUES(21,'launch-21',1,1,2,'{}')`); err == nil {
		t.Fatal("grant with two subjects was accepted")
	}
	if err := rawTryWithForeignKeys(t, path, `INSERT INTO authority_grants(execution_id,launch_id,version,issued_at,expires_at,policy) VALUES(21,NULL,1,1,NULL,'{}')`); err == nil {
		t.Fatal("execution grant without expiry was accepted")
	}
	if err := rawTryWithForeignKeys(t, path, `INSERT INTO authority_grants(execution_id,launch_id,version,issued_at,expires_at,policy) VALUES(NULL,'launch-off-21',1,1,NULL,'{}')`); err == nil {
		t.Fatal("Off launch grant was accepted")
	}
}

func TestSchema21RefusesMalformedWithoutRestamping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	aReleasedSchema21Database(t, path)
	// Corrupt the released shape without changing its stamp; Open must refuse
	// instead of treating familiar object names as proof of compatibility.
	rawExec(t, path, `CREATE TABLE unrelated_schema_change(id TEXT)`)
	if _, err := Open(context.Background(), Config{Path: path, Key: schemaTestKey(), Budget: testBudgetInternal()}); err == nil {
		t.Fatal("malformed schema 21 was accepted")
	}
	if got := rawUserVersion(t, path); got != 21 {
		t.Fatalf("malformed schema was restamped to %d", got)
	}
}
