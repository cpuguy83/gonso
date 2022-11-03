package gonso

import (
	"context"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	p := NewPool(NS_NET, nil)

	if p.Len() != 0 {
		t.Errorf("expected pool to be empty")
	}

	s, err := p.Get()
	if err != nil {
		t.Fatal(err)
	}
	p.Put(s)

	if p.Len() != 1 {
		t.Fatal("expected pool to have one set")
	}

	s, err = p.Get()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	if p.Len() != 0 {
		t.Errorf("expected pool to be empty")
	}

	p.notify = func() {
		p.cvar.Signal()
	}

	ctx, cancelP := p.Run(context.Background(), 4)
	defer cancelP()

	ctxT, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	waitForPool(t, ctxT, p, 4)
	cancel()

	p.Get()
	ctxT, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	waitForPool(t, ctxT, p, 4)

	cancelP()
	waitForPool(t, ctxT, p, 0)
}

func waitForPool(t *testing.T, ctx context.Context, p *Pool, n int) {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()

	for len(p.sets) < n {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		p.cvar.Wait()
	}
}
