package main

import (
	"strings"
	"testing"
)

func TestReleaseRefspecNeverForcesPublishedTagMove(t *testing.T) {
	refspec := releaseRefspec("v1.2.3")
	if refspec != "refs/tags/v1.2.3:refs/happylearn-update/releases/1.2.3" {
		t.Fatalf("refspec = %q", refspec)
	}
	if strings.HasPrefix(refspec, "+") {
		t.Fatalf("release refspec permits forced tag move: %q", refspec)
	}
	if got := releaseRefspec("v1.2.3-rc.1"); got != "" {
		t.Fatalf("unsafe tag refspec = %q", got)
	}
}

func TestPublishedAnnotatedTagRejectsRetaggingSameCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	if publishedTagUnchanged(strings.Repeat("b", 40), strings.Repeat("c", 40), commit, commit) {
		t.Fatal("retagged annotated object on the same commit was accepted")
	}
	if !publishedTagUnchanged(strings.Repeat("b", 40), strings.Repeat("b", 40), commit, commit) {
		t.Fatal("unchanged annotated tag was rejected")
	}
}

func TestConfiguredBranchRefspecIsIsolatedAndNeverForced(t *testing.T) {
	refspec := configuredBranchRefspec("release/stable")
	if refspec != "refs/heads/release/stable:refs/happylearn-update/branches/release/stable" {
		t.Fatalf("refspec = %q", refspec)
	}
	if strings.HasPrefix(refspec, "+") {
		t.Fatalf("branch refspec permits forced history rewrite: %q", refspec)
	}
	if got := configuredBranchRefspec("../unsafe"); got != "" {
		t.Fatalf("unsafe branch refspec = %q", got)
	}
}

func TestMigrationChangesBlockLocalOTA(t *testing.T) {
	for _, output := range []string{
		"A\tdb/migrations/0042_new.sql",
		"M\tdb/migrations/0001_initial.sql",
		"D\tdb/migrations/0002_old.sql",
		"R100\tdb/migrations/old.sql\tdb/migrations/new.sql",
	} {
		if !hasMigrationChanges(output) {
			t.Errorf("migration diff %q was not blocked", output)
		}
	}
	for _, output := range []string{"", "M\tinternal/updates/service.go", "A\tdocs/migrations.md"} {
		if hasMigrationChanges(output) {
			t.Errorf("non-migration diff %q was blocked", output)
		}
	}
}

func TestOTAControlPlaneChangesRequireFullRedeploy(t *testing.T) {
	for _, output := range []string{
		"M\tcmd/update-agent/agent.go",
		"M\tinternal/updates/model.go",
		"M\tdeploy/Dockerfile.update-agent",
		"M\tdeploy/compose.dev.yml",
		"M\tdeploy/compose.github.yml",
		"M\tscripts/deploy-from-github.sh",
	} {
		if !hasOTAControlPlaneChanges(output) {
			t.Errorf("control-plane diff %q was not blocked", output)
		}
	}
	for _, output := range []string{"", "M\tinternal/questions/service.go", "M\tDockerfile"} {
		if hasOTAControlPlaneChanges(output) {
			t.Errorf("ordinary application diff %q was blocked", output)
		}
	}
}

func TestUnsafeCheckoutBlockClearsUpdateAvailable(t *testing.T) {
	for name, input := range map[string]struct {
		branchMatches bool
		dirty         bool
	}{
		"branch mismatch": {branchMatches: false},
		"dirty checkout":  {branchMatches: true, dirty: true},
	} {
		t.Run(name, func(t *testing.T) {
			status := initialStatus(config{ref: "master"})
			status.State = stateAvailable
			status.UpdateAvailable = true
			status.Phase = phaseComplete
			status.Progress = 100
			blockUnsafeCheckout(&status, input.branchMatches, input.dirty)
			if status.State != stateBlocked || status.UpdateAvailable {
				t.Fatalf("status = %+v", status)
			}
			if !validPersistedStatus(status) {
				t.Fatalf("blocked status is not persistable: %+v", status)
			}
		})
	}
}
