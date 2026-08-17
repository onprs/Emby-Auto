package database

import (
	"context"
	"testing"
)

func TestRegisterBeforeCommitHookKeepsOrderAndReplacesDuplicateName(t *testing.T) {
	transactor := &Transactor{}
	calls := make([]string, 0, 2)
	transactor.RegisterBeforeCommitHook("first", func(context.Context, TxScope) error {
		calls = append(calls, "obsolete")
		return nil
	})
	transactor.RegisterBeforeCommitHook("second", func(context.Context, TxScope) error {
		calls = append(calls, "second")
		return nil
	})
	transactor.RegisterBeforeCommitHook("first", func(context.Context, TxScope) error {
		calls = append(calls, "first")
		return nil
	})
	transactor.RegisterBeforeCommitHook("", nil)

	hooks := transactor.beforeCommitHooks()
	if len(hooks) != 2 || hooks[0].name != "first" || hooks[1].name != "second" {
		t.Fatalf("hooks = %#v, want first/second without duplicate", hooks)
	}
	for _, registered := range hooks {
		if err := registered.hook(context.Background(), TxScope{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 2 || calls[0] != "first" || calls[1] != "second" {
		t.Fatalf("calls = %v, want [first second]", calls)
	}
}

func TestBeforeCommitHooksReturnsIndependentSnapshot(t *testing.T) {
	transactor := &Transactor{}
	transactor.RegisterBeforeCommitHook("stable", func(context.Context, TxScope) error { return nil })

	snapshot := transactor.beforeCommitHooks()
	snapshot[0].name = "changed"
	current := transactor.beforeCommitHooks()
	if current[0].name != "stable" {
		t.Fatalf("registered hook name = %q, want stable", current[0].name)
	}
}
