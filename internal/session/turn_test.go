package session

import (
	"errors"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func wantErr(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidTurnState) {
		t.Fatalf("expected ErrInvalidTurnState, got %v", err)
	}
}

func TestTurnStateMachine(t *testing.T) {
	t.Run("full happy path", func(t *testing.T) {
		tr := &Turn{Status: TurnPending}
		must(t, tr.Start())
		if tr.Status != TurnRunning || tr.StartedAt.IsZero() {
			t.Fatalf("start failed: %+v", tr)
		}
		must(t, tr.Wait())
		if tr.Status != TurnWaiting {
			t.Fatalf("wait failed: %+v", tr)
		}
		must(t, tr.Resume())
		if tr.Status != TurnRunning {
			t.Fatalf("resume failed: %+v", tr)
		}
		must(t, tr.Complete("final answer"))
		if tr.Status != TurnDone || tr.Output != "final answer" || tr.FinishedAt == nil {
			t.Fatalf("complete failed: %+v", tr)
		}
	})

	t.Run("complete directly from running", func(t *testing.T) {
		tr := &Turn{Status: TurnPending}
		must(t, tr.Start())
		must(t, tr.Complete("ok"))
		if tr.Status != TurnDone || tr.FinishedAt == nil {
			t.Fatalf("complete failed: %+v", tr)
		}
	})

	t.Run("fail and cancel", func(t *testing.T) {
		tr := &Turn{Status: TurnPending}
		must(t, tr.Start())
		must(t, tr.Fail("boom"))
		if tr.Status != TurnFailed || tr.Error != "boom" || tr.FinishedAt == nil {
			t.Fatalf("fail failed: %+v", tr)
		}

		tr = &Turn{Status: TurnPending}
		must(t, tr.Cancel())
		if tr.Status != TurnCanceled || tr.FinishedAt == nil {
			t.Fatalf("cancel failed: %+v", tr)
		}
	})

	t.Run("invalid transitions", func(t *testing.T) {
		done := &Turn{Status: TurnDone}
		wantErr(t, done.Start())
		wantErr(t, done.Wait())
		wantErr(t, done.Resume())
		wantErr(t, done.Complete("late"))
		wantErr(t, done.Fail("late"))
		wantErr(t, done.Cancel())

		pending := &Turn{Status: TurnPending}
		wantErr(t, pending.Complete("early"))
		wantErr(t, pending.Fail("early"))
		wantErr(t, pending.Resume())

		running := &Turn{Status: TurnRunning}
		wantErr(t, running.Resume())
	})
}

func TestStepStateMachine(t *testing.T) {
	st := &Step{}
	must(t, st.Begin())
	if st.Status != StepRunning || st.StartedAt.IsZero() {
		t.Fatalf("begin failed: %+v", st)
	}
	must(t, st.Wait())
	if st.Status != StepWaiting {
		t.Fatalf("wait failed: %+v", st)
	}
	must(t, st.Succeed("result"))
	if st.Status != StepDone || st.Result != "result" || st.FinishedAt == nil {
		t.Fatalf("succeed failed: %+v", st)
	}
	wantErr(t, st.Begin())
	wantErr(t, st.Succeed("twice"))

	failed := &Step{Status: StepRunning}
	must(t, failed.Fail("err"))
	if failed.Status != StepFailed || failed.FinishedAt == nil {
		t.Fatalf("fail failed: %+v", failed)
	}
	wantErr(t, failed.Wait())
}

// TestMultiTurnReplay 模拟多轮 Session 的 Turn 独立推进，保证状态互不串扰且
// 每轮都能重放到 done（P1.7 回放的模型前提）。
func TestMultiTurnReplay(t *testing.T) {
	start := time.Now()
	t1 := &Turn{ID: "t1", SessionID: "s1", Status: TurnPending, Input: "first"}
	t2 := &Turn{ID: "t2", SessionID: "s1", Status: TurnPending, Input: "second"}
	if !t1.StartedAt.IsZero() || !t2.StartedAt.IsZero() {
		t.Fatalf("pending turns must not have started timestamps")
	}

	must(t, t1.Start())
	must(t, t1.Complete("replay 1"))
	must(t, t2.Start())
	must(t, t2.Complete("replay 2"))

	if t1.Status != TurnDone || t2.Status != TurnDone {
		t.Fatalf("turns not both done: %+v %+v", t1, t2)
	}
	if t1.Output != "replay 1" || t2.Output != "replay 2" {
		t.Fatalf("outputs crossed: %q %q", t1.Output, t2.Output)
	}
	if t1.FinishedAt == nil || t2.FinishedAt == nil {
		t.Fatalf("finished timestamps missing")
	}
	if t1.FinishedAt.Before(start) || t2.FinishedAt.Before(start) {
		t.Fatalf("finished timestamps before start: %v %v", t1.FinishedAt, start)
	}
}
