package wallet

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestInitKeepsTheServerError pins that Init wraps the server's error
// instead of flattening it to text, so a caller can tell a server it could
// not reach (codes.Unavailable) from one that answered with an error.
func TestInitKeepsTheServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	w := &wallet{identity: probeIdentity{}}
	err := w.Init(ctx, InitArgs{
		ServerUrl:   "127.0.0.1:1",
		Password:    "password",
		ExplorerURL: "http://127.0.0.1:1",
	})
	require.Error(t, err)

	st, ok := status.FromError(err)
	require.True(t, ok, "the gRPC status was flattened: %v", err)
	require.Equal(t, codes.Unavailable, st.Code())
}
