package access

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	memqlv1 "github.com/znasllc-io/memql/component/grpc/gen"
	sdkclient "github.com/znasllc-io/memql/sdk/go/client"

	"github.com/znasllc-io/memql-cockpit/internal/auth"
	"github.com/znasllc-io/memql-cockpit/internal/config"
)

// callTimeout bounds the dial-handshake-ask-answer round trip. A person is
// waiting at a prompt; a cluster that has not answered by now is a fact worth
// printing rather than something to keep waiting for.
//
// IT DELIBERATELY DOES NOT COVER THE SIGN-IN. EnsureValidToken is the
// interactive one here, so an expired token opens a browser -- and that is
// human-paced: find the window, pick an account, approve. Twenty seconds is a
// good ceiling for "the cluster did not answer" and a terrible one for "the
// human has not finished logging in", where it would kill the sign-in partway
// through with a deadline error naming nothing the person did wrong.
const callTimeout = 20 * time.Second

// ensureToken is auth.EnsureValidToken, indirected so the deadline placement
// above is testable without a cluster or a browser.
var ensureToken = auth.EnsureValidToken

// HandleCommand runs `memql access`.
func HandleCommand(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

// run is HandleCommand with its two streams injected, so the exit codes and
// the sentences are testable without a subprocess.
func run(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("access", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() { printUsage(errOut) }
	if err := fs.Parse(args); err != nil {
		// HELP THAT WAS ASKED FOR IS A SUCCESS. flag reports it as an error
		// like any other parse failure, and passing that through would break
		// `memql access --help && ...` and make the command look broken in a
		// script that checks exit codes.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	name := ""
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	clusters, err := config.LoadClusters()
	if err != nil {
		fmt.Fprintf(errOut, "ERROR: %v\n", err)
		return 1
	}
	cluster, err := resolveCluster(clusters, name)
	if err != nil {
		fmt.Fprintf(errOut, "ERROR: %v\n", err)
		return 1
	}

	// Background, not a deadlined context: Fetch bounds its own round trip and
	// leaves the sign-in unbounded on purpose.
	summary, err := Fetch(context.Background(), cluster)
	if err != nil {
		fmt.Fprintf(errOut, "ERROR: %v\n", err)
		return 1
	}

	if *asJSON {
		if err := RenderJSON(out, summary, cluster.Name); err != nil {
			fmt.Fprintf(errOut, "ERROR: %v\n", err)
			return 1
		}
		return 0
	}
	Render(out, summary, cluster.Name)
	return 0
}

// Fetch signs in if needed, asks the cluster for the caller's access record,
// and decodes it.
//
// IT ASKS THE CLUSTER RATHER THAN DECODING THE BEARER. The cockpit does not
// parse its own tokens -- the standing client rule the wire's `session_id` and
// `display_name` fields exist to serve -- because a claim is what was true when
// the token was minted, and a role changed since is exactly what somebody
// running this command is trying to find out.
func Fetch(ctx context.Context, cluster config.ClusterConfig) (Summary, error) {
	cluster = config.WithLocalDefault(cluster)
	if cluster.Endpoint == "" {
		return Summary{}, fmt.Errorf("cluster %q has no endpoint. Re-run `memql cluster add <domain>` to register it", cluster.Name)
	}

	// INTERACTIVE on purpose, unlike the worker's paths: a person typed this,
	// so a browser sign-in is the right answer to an expired token rather than
	// a window nobody will open.
	token, err := ensureToken(ctx, cluster)
	if err != nil {
		// Named, because the cluster is often IMPLICIT here -- resolved from
		// selected_cluster rather than typed -- and "login: context deadline
		// exceeded" on its own does not say which one is unreachable.
		return Summary{}, fmt.Errorf("sign in to %q: %w", cluster.Name, err)
	}

	// The deadline starts HERE, once the human part is done.
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	conn, err := sdkclient.Connect(ctx, sdkclient.ConnectConfig{
		Endpoint: cluster.Endpoint,
		Token:    token,
	})
	if err != nil {
		return Summary{}, fmt.Errorf("connect to %s: %w", cluster.Endpoint, err)
	}
	defer conn.Close()

	resp, err := conn.Dispatcher().SendAndWait(ctx, &memqlv1.MemqlClientMessage{
		Payload: &memqlv1.MemqlClientMessage_MyAccess{MyAccess: &memqlv1.MyAccessMsg{}},
	})
	if err != nil {
		return Summary{}, fmt.Errorf("ask %s who I am: %w", cluster.Name, err)
	}
	// A REFUSAL IS NOT A TRANSPORT ERROR. It arrives as a QueryError inside a
	// perfectly good response, so a caller that only checked `err` would render
	// an empty record as "you hold nothing" -- which is the same shape as the
	// answer it would give a person who really does.
	if qErr := resp.GetQueryError(); qErr != nil {
		return Summary{}, fmt.Errorf("%s refused: %s", cluster.Name, qErr.GetError().GetMessage())
	}
	result := resp.GetMyAccessResult()
	if result == nil {
		return Summary{}, fmt.Errorf("%s answered without an access record", cluster.Name)
	}
	return Decode(result), nil
}

// resolveCluster picks the cluster to report on.
//
// It REFUSES to guess between several. Reporting somebody's access on a
// cluster they did not name is worse than asking, because the answer looks
// exactly like the one they wanted -- and the whole point of this command is
// to be believed about which permissions are real.
func resolveCluster(file *config.ClustersFile, name string) (config.ClusterConfig, error) {
	registered := clusterNames(file)

	if name != "" {
		c, ok := file.Get(name)
		if !ok {
			if len(registered) == 0 {
				return config.ClusterConfig{}, fmt.Errorf("cluster %q is not registered, and neither is any other. Register one with `memql cluster add <domain>`", name)
			}
			return config.ClusterConfig{}, fmt.Errorf("cluster %q is not registered. Registered clusters: %s", name, strings.Join(registered, ", "))
		}
		return c, nil
	}

	if len(registered) == 0 {
		return config.ClusterConfig{}, fmt.Errorf("no clusters are registered. Register one with `memql cluster add <domain>`")
	}

	if sel := strings.TrimSpace(file.SelectedCluster); sel != "" {
		c, ok := file.Get(sel)
		if !ok {
			// Worth a sentence rather than a silent fallback: the operator's
			// chosen working cluster has gone, and every other tool sharing
			// clusters.yaml is about to behave oddly too.
			return config.ClusterConfig{}, fmt.Errorf("the selected cluster %q is no longer registered. Name one explicitly: %s", sel, strings.Join(registered, ", "))
		}
		return c, nil
	}

	if len(registered) == 1 {
		c, _ := file.Get(registered[0])
		return c, nil
	}

	return config.ClusterConfig{}, fmt.Errorf("several clusters are registered and none is selected. Name one: %s", strings.Join(registered, ", "))
}

func clusterNames(file *config.ClustersFile) []string {
	if file == nil {
		return nil
	}
	names := make([]string, 0, len(file.Clusters))
	for _, c := range file.Clusters {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: memql access [<cluster>] [--json]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Print what this cluster says about the signed-in credential on this")
	fmt.Fprintln(w, "machine: the user it resolves to, the role held (slug, name and rank),")
	fmt.Fprintln(w, "group memberships, and account scope.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "With no <cluster>, the selected cluster in ~/.memql/clusters.yaml is used,")
	fmt.Fprintln(w, "or the only registered one. Several with none selected is an error rather")
	fmt.Fprintln(w, "than a guess.")
}
