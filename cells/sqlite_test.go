package cells_test

import (
	"path/filepath"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/sqlitedb"
)

// TestServiceOnSQLite runs the service tests on the production store, both
// on a file and in memory.
func TestServiceOnSQLite(t *testing.T) {
	tests := map[string]func(*testing.T){
		"SetDesiredWithStaleVersionConflicts":      TestSetDesiredWithStaleVersionConflicts,
		"ReportedCauseAndManual":                   TestReportedCauseAndManual,
		"WatchReplaysThenStreams":                  TestWatchReplaysThenStreams,
		"WatchFromSeqSkipsOlder":                   TestWatchFromSeqSkipsOlder,
		"SlowWatcherLosesNothing":                  TestSlowWatcherLosesNothing,
		"AnswerAfterTTLExpires":                    TestAnswerAfterTTLExpires,
		"ExpireDueMarksOnlyDue":                    TestExpireDueMarksOnlyDue,
		"AnswerTwiceIsRejected":                    TestAnswerTwiceIsRejected,
		"ReconcileReappliesAfterCrash":             TestReconcileReappliesAfterCrash,
		"ReconcileContinuesPastFailures":           TestReconcileContinuesPastFailures,
		"ExternalReportBeforeCommandIsNotManual":   TestExternalReportBeforeCommandIsNotManual,
		"SetDesiredReturnsTheCellAfterASyncReport": TestSetDesiredReturnsTheCellAfterASyncReport,
		"DefineAcceptsDashedIDs":                   TestDefineAcceptsDashedIDs,
		"DefineOwnedCreatesWithOwnerAndAbout":      TestDefineOwnedCreatesWithOwnerAndAbout,
		"DefineOwnedByAnotherOwnerIsRefused":       TestDefineOwnedByAnotherOwnerIsRefused,
		"DefineOwnedSameOwnerUpdatesAbout":         TestDefineOwnedSameOwnerUpdatesAbout,
		"DefineOwnedOfAnotherKindConflicts":        TestDefineOwnedOfAnotherKindConflicts,
		"DefineOwnedTakesAnOwnerlessCell":          TestDefineOwnedTakesAnOwnerlessCell,
		"DefineOwnedValidates":                     TestDefineOwnedValidates,
		"DisownThenRedefineKeepsHistory":           TestDisownThenRedefineKeepsHistory,
		"ListReturnsOwnerAndAbout":                 TestListReturnsOwnerAndAbout,
		"HistorySinceIsOldestFirstAndLimited":      TestHistorySinceIsOldestFirstAndLimited,
	}
	factories := map[string]func(*testing.T) cells.Store{
		"file":   func(t *testing.T) cells.Store { return sqliteStore(t, filepath.Join(t.TempDir(), "cells.db")) },
		"memory": func(t *testing.T) cells.Store { return sqliteStore(t, ":memory:") },
	}
	orig := newStore
	t.Cleanup(func() { newStore = orig })
	for fname, factory := range factories {
		newStore = factory
		for name, test := range tests {
			t.Run(fname+"/"+name, test)
		}
	}
}

func sqliteStore(t *testing.T, path string) cells.Store {
	t.Helper()
	db, err := sqlitedb.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := sqlstore.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
