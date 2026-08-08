package repository

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type unixRequest struct {
	Path   string
	Header http.Header
	Body   string
}

type unixTestServer struct {
	SocketPath string
	Requests   *[]unixRequest
	server     *http.Server
	listener   net.Listener
}

func newUnixHTTPTestServer(t *testing.T, status int, body string) *unixTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	requests := make([]unixRequest, 0, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		requests = append(requests, unixRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: string(payload)})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	server := &http.Server{Handler: handler}
	result := &unixTestServer{SocketPath: listener.Addr().String(), Requests: &requests, server: server, listener: listener}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
	})
	return result
}

func testImageUpdaterClient(server *unixTestServer) *imageUpdaterClient {
	client := NewImageUpdaterClient("test-socket", "client-secret").(*imageUpdaterClient)
	client.httpClient.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.SocketPath)
		},
		DisableKeepAlives: true,
	}
	return client
}

func TestImageUpdaterClientStageSendsVersionAndToken(t *testing.T) {
	server := newUnixHTTPTestServer(t, http.StatusOK, `{"status":"staged"}`)
	client := testImageUpdaterClient(server)
	require.NoError(t, client.Stage(context.Background(), "0.1.172"))
	require.Len(t, *server.Requests, 1)
	req := (*server.Requests)[0]
	require.Equal(t, "/v1/stage", req.Path)
	require.Equal(t, "Bearer client-secret", req.Header.Get("Authorization"))
	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(req.Body), &payload))
	require.Equal(t, map[string]string{"version": "0.1.172"}, payload)
}

func TestImageUpdaterClientApplySendsNoUserControlledFields(t *testing.T) {
	server := newUnixHTTPTestServer(t, http.StatusAccepted, `{"status":"applying"}`)
	client := testImageUpdaterClient(server)
	require.NoError(t, client.Apply(context.Background()))
	require.Len(t, *server.Requests, 1)
	req := (*server.Requests)[0]
	require.Equal(t, "/v1/apply", req.Path)
	require.Equal(t, "Bearer client-secret", req.Header.Get("Authorization"))
	require.Empty(t, req.Body)
}

func TestImageUpdaterClientRejectsInvalidVersionBeforeDial(t *testing.T) {
	client := NewImageUpdaterClient(filepath.Join(t.TempDir(), "missing.sock"), "client-secret")
	err := client.Stage(context.Background(), "0.1.172; rm -rf /")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid version")
}

func TestImageUpdaterClientRejectsNonSuccess(t *testing.T) {
	server := newUnixHTTPTestServer(t, http.StatusConflict, `{"error":"busy"}`)
	client := testImageUpdaterClient(server)
	err := client.Apply(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "409")
	require.NotContains(t, err.Error(), "secret")
}

func TestImageUpdaterClientRequiresSocketAndToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		socket string
		token  string
	}{
		{name: "socket", socket: "", token: "secret"},
		{name: "token", socket: "/tmp/updater.sock", token: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewImageUpdaterClient(tc.socket, tc.token)
			require.Error(t, client.Apply(context.Background()))
		})
	}
}
