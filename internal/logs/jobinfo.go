package logs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// JobInfo is what a dashboard needs to name a running job: where it comes
// from, not just the display name the runner prints.
type JobInfo struct {
	Name     string // the job's display name
	Repo     string // owner/name
	Workflow string // the workflow's name
	RunID    string
}

// The runner starts a worker process per job, and the worker writes its own
// _diag/Worker_<yyyymmdd-hhmmss>-utc.log. Near its top it logs the job message
// GitHub sent — a JSON document on the lines after "Job message:" — whose
// github context carries the repository, workflow and run id. Secret values in
// it are masked by the runner before logging, and only those three are read.
var reWorkerLog = regexp.MustCompile(`Worker_(\d{8}-\d{6})-utc\.log$`)

// workerLogWindow is how far a worker log's start may trail the "Running job"
// line: the runner spawns the worker within a second or two.
const workerLogWindow = 30 * time.Second

// FindJobInfo reads the job message of the job that started on the worker at
// start. path is the worker log an earlier call resolved for this job, or
// empty on the first one; it is looked up again only when empty, so a job
// already found in _diag is not globbed for again while its message is still
// being written. It reports false while no worker log is there yet, or one
// is but not yet written far enough to parse — callers retry, passing the
// returned path back in either way.
func FindJobInfo(workerDir string, start time.Time, path string) (JobInfo, string, bool) {
	if path == "" {
		path = workerLogFor(filepath.Join(workerDir, "_diag"), start)
		if path == "" {
			return JobInfo{}, "", false
		}
	}
	info, ok := readJobMessage(path)
	return info, path, ok
}

// workerLogFor picks the worker log that began closest after start.
func workerLogFor(diag string, start time.Time) string {
	matches, _ := filepath.Glob(filepath.Join(diag, "Worker_*.log"))
	best, bestGap := "", workerLogWindow+1
	for _, m := range matches {
		sm := reWorkerLog.FindStringSubmatch(m)
		if sm == nil {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", sm[1], time.UTC)
		if err != nil {
			continue
		}
		// One second of slack before: both stamps are truncated to the second.
		gap := t.Sub(start)
		if gap >= -time.Second && gap <= workerLogWindow && gap < bestGap {
			best, bestGap = m, gap
		}
	}
	return best
}

func readJobMessage(path string) (JobInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return JobInfo{}, false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var buf strings.Builder
	in := false
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "\ufeff")
		if !in {
			in = strings.HasSuffix(line, "] Job message:")
			continue
		}
		if reDiagStamp.MatchString(line) {
			return parseJobMessage(buf.String())
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return JobInfo{}, false // the message is not fully written yet
}

func parseJobMessage(doc string) (JobInfo, bool) {
	var msg struct {
		JobDisplayName string `json:"jobDisplayName"`
		ContextData    struct {
			Github struct {
				D []struct {
					K string          `json:"k"`
					V json.RawMessage `json:"v"`
				} `json:"d"`
			} `json:"github"`
		} `json:"contextData"`
	}
	if err := json.Unmarshal([]byte(doc), &msg); err != nil {
		return JobInfo{}, false
	}
	info := JobInfo{Name: msg.JobDisplayName}
	for _, e := range msg.ContextData.Github.D {
		var v string
		if json.Unmarshal(e.V, &v) != nil {
			continue
		}
		switch e.K {
		case "repository":
			info.Repo = v
		case "workflow":
			info.Workflow = v
		case "run_id":
			info.RunID = v
		}
	}
	return info, true
}
