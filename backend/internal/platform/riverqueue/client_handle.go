package riverqueue

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// ClientHandle lets transaction services be assembled before the River client,
// whose construction itself requires the fully assembled worker registry.
type ClientHandle struct {
	mutex  sync.RWMutex
	client *river.Client[pgx.Tx]
}

func NewClientHandle() *ClientHandle {
	return &ClientHandle{}
}

func (handle *ClientHandle) Bind(client *river.Client[pgx.Tx]) error {
	if client == nil {
		return fmt.Errorf("river client is required")
	}
	handle.mutex.Lock()
	defer handle.mutex.Unlock()
	if handle.client != nil {
		return fmt.Errorf("river client handle is already bound")
	}
	handle.client = client
	return nil
}

func (handle *ClientHandle) InsertTx(
	ctx context.Context,
	tx pgx.Tx,
	args river.JobArgs,
	options *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	handle.mutex.RLock()
	client := handle.client
	handle.mutex.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("river client handle is not bound")
	}
	return client.InsertTx(ctx, tx, args, options)
}
