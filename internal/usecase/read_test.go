package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hasanmahira/notifd/internal/domain"
)

func TestService_Get_NotFound(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	_, err := svc.Get(context.Background(), domain.NewID())
	if err == nil || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestService_Get_Found(t *testing.T) {
	svc, repo, _ := newServiceForTest(t)
	id := seed(t, repo)
	got, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserNotification.ID != id {
		t.Fatalf("id mismatch: want %s, got %s", id, got.UserNotification.ID)
	}
}

func TestService_List_ClampsLimit(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	// repo.List is called with q; our fake ignores limit but returns [].
	// We pass limit=10000 to trigger the clamp branch.
	_, _, err := svc.List(context.Background(), ListQuery{Limit: 10000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestService_GetBatch_NotFound(t *testing.T) {
	svc, _, _ := newServiceForTest(t)
	_, err := svc.GetBatch(context.Background(), domain.NewID())
	if err == nil || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
