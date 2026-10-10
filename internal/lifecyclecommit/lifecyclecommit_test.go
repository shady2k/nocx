package lifecyclecommit

import (
	"context"
	"reflect"
	"testing"
)

func TestEffectsRunInOrderOnCommitAndNotAtAllOtherwise(t *testing.T) {
	for _, committed := range []bool{true, false} {
		ctx, q := Begin(context.Background())
		var ran []string
		OnCommit(ctx, func() { ran = append(ran, "first") })
		After(ctx, "k", func(c bool) {
			ran = append(ran, "second")
			// A consequence queued while the queue runs runs after it.
			OnCommit(ctx, func() { ran = append(ran, "fourth") })
		})
		After(ctx, "k", func(bool) { ran = append(ran, "a duplicate key adds nothing") })
		OnCommit(ctx, func() { ran = append(ran, "third") })
		if len(ran) != 0 {
			t.Fatalf("effects ran before the frame ended: %v", ran)
		}
		q.End(committed)
		want := []string{"first", "second", "third", "fourth"}
		if !committed {
			want = []string{"second"}
		}
		if !reflect.DeepEqual(ran, want) {
			t.Fatalf("committed=%v: ran %v, want %v", committed, ran, want)
		}
	}
}

func TestOutsideAFrameAnEffectRunsAtOnce(t *testing.T) {
	ran := false
	OnCommit(context.Background(), func() { ran = true })
	if !ran {
		t.Fatal("an effect outside a frame did not run at once")
	}
}
