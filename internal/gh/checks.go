package gh

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Check is one CI check on a PR's head commit: a check run (GitHub Actions
// or an app) or a commit status.
type Check struct {
	Name        string
	Workflow    string // the Actions workflow it belongs to, if any
	State       string // PASS, FAIL, PENDING or SKIPPED
	Required    bool   // branch protection requires it to pass before merging
	URL         string
	Started     time.Time
	Finished    time.Time // zero while it runs, and for commit statuses
	Title       string    // what the check said about its result
	Summary     string    // markdown
	Annotations []Annotation
	JobID       int64 // the Actions job, whose log JobLogTail reads; 0 otherwise
}

// Annotation is a check's note on a line, e.g. a compile error or a failed
// assertion.
type Annotation struct {
	Path    string
	Line    int
	Level   string // NOTICE, WARNING or FAILURE
	Message string
}

// checkNode is a check context as the GraphQL API returns it.
type checkNode struct {
	Typename    string    `json:"__typename"`
	DatabaseID  int64     `json:"databaseId"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
	DetailsURL  string    `json:"detailsUrl"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	IsRequired  bool      `json:"isRequired"`
	CheckSuite  *struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Annotations struct {
		Nodes []struct {
			Path            string `json:"path"`
			Message         string `json:"message"`
			AnnotationLevel string `json:"annotationLevel"`
			Location        struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"location"`
		} `json:"nodes"`
	} `json:"annotations"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	TargetURL   string    `json:"targetUrl"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (n checkNode) check() Check {
	if n.Typename == "StatusContext" {
		state := map[string]string{"SUCCESS": "PASS", "FAILURE": "FAIL", "ERROR": "FAIL"}[n.State]
		if state == "" {
			state = "PENDING"
		}
		return Check{Name: n.Context, State: state, Required: n.IsRequired, URL: n.TargetURL, Started: n.CreatedAt, Title: n.Description}
	}
	c := Check{Name: n.Name, Required: n.IsRequired, URL: n.DetailsURL, Started: n.StartedAt, Finished: n.CompletedAt,
		Title: n.Title, Summary: n.Summary}
	switch {
	case n.Status != "COMPLETED":
		c.State, c.Finished = "PENDING", time.Time{}
	case n.Conclusion == "SUCCESS" || n.Conclusion == "NEUTRAL":
		c.State = "PASS"
	case n.Conclusion == "SKIPPED":
		c.State = "SKIPPED"
	default:
		c.State = "FAIL"
	}
	if n.CheckSuite != nil && n.CheckSuite.WorkflowRun != nil {
		c.Workflow = n.CheckSuite.WorkflowRun.Workflow.Name
		c.JobID = n.DatabaseID
	}
	for _, a := range n.Annotations.Nodes {
		c.Annotations = append(c.Annotations, Annotation{Path: a.Path, Line: a.Location.Start.Line, Level: a.AnnotationLevel, Message: a.Message})
	}
	return c
}

// latestChecks converts check contexts, keeping only the latest run of each
// check in each workflow: a commit can carry several runs of one check (a
// re-run, or another event), and an older failure no longer counts.
func latestChecks(nodes []checkNode) []Check {
	var out []Check
	at := map[string]int{}
	for _, n := range nodes {
		c := n.check()
		k := c.Workflow + "\x00" + c.Name
		if i, ok := at[k]; ok {
			if c.Started.After(out[i].Started) {
				out[i] = c
			}
			continue
		}
		at[k] = len(out)
		out = append(out, c)
	}
	return out
}

// logStamp is the timestamp GitHub Actions puts at the start of each log line.
var logStamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+Z ?`)

// JobLogTail returns the n lines of a GitHub Actions job's log that lead up
// to its first error, without timestamps and group markers, and the name of
// the step that failed. Without an error it is the last n lines. Reading
// stops at the error, so the post-job cleanup that ends every log is left
// out; logs can be megabytes, and only n lines are kept while reading.
func (c *Client) JobLogTail(repo string, jobID int64, n int) (step string, lines []string, err error) {
	var job struct {
		Steps []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	}
	if err := c.rest.Get(fmt.Sprintf("repos/%s/actions/jobs/%d", repo, jobID), &job); err != nil {
		return "", nil, fmt.Errorf("job %d: %w", jobID, err)
	}
	for _, s := range job.Steps {
		if s.Conclusion == "failure" {
			step = s.Name
			break
		}
	}
	resp, err := c.rest.Request("GET", fmt.Sprintf("repos/%s/actions/jobs/%d/logs", repo, jobID), nil)
	if err != nil {
		return step, nil, fmt.Errorf("job %d log: %w", jobID, err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := strings.TrimRight(logStamp.ReplaceAllString(sc.Text(), ""), "\r ")
		if l == "" || strings.HasPrefix(l, "##[group]") || strings.HasPrefix(l, "##[endgroup]") {
			continue
		}
		msg, failed := strings.CutPrefix(Clean(l), "##[error]")
		lines = append(lines, msg)
		if len(lines) > n {
			lines = lines[1:]
		}
		if failed {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return step, lines, fmt.Errorf("job %d log: %w", jobID, err)
	}
	return step, lines, nil
}
