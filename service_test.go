package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitdomain "iden/internal/domain/git"
	workspace "iden/internal/domain/workspace"
)



func TestGetDiffPreviewReturnsUntrackedPatch(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createStatusFixtureRepository(t)
	service := newTestService(repositoryPath)

	preview, err := service.GetDiffPreview(gitdomain.GetDiffPreviewRequest{
		RelativePath: "new.txt",
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetDiffPreview returned error: %v", err)
	}

	if preview.HasStagedPatch {
		t.Fatalf("expected no staged patch for untracked file, got %+v", preview)
	}
	if !preview.HasWorktreePatch {
		t.Fatalf("expected worktree patch for untracked file, got %+v", preview)
	}
	if !preview.WorktreeView.Available {
		t.Fatalf("expected worktree diff view for untracked file, got %+v", preview)
	}
	if preview.WorktreeView.Original.Exists {
		t.Fatalf("expected empty original side for untracked file, got %+v", preview.WorktreeView)
	}
	if !preview.WorktreeView.Modified.Exists || !containsAll(preview.WorktreeView.Modified.Content, "new file") {
		t.Fatalf("expected modified side content for untracked file, got %+v", preview.WorktreeView)
	}
	if !containsAll(preview.WorktreePatch, "new file mode", "+++ b/new.txt") {
		t.Fatalf("expected untracked patch header, got %q", preview.WorktreePatch)
	}
}

func TestGetRepositoryStatusExpandsUntrackedDirectoriesToFiles(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "tracked.txt"), []byte("tracked\n"))
	runGitCommand(t, repositoryPath, "add", "tracked.txt")
	runGitCommand(t, repositoryPath, "commit", "-m", "init")

	if err := os.MkdirAll(filepath.Join(repositoryPath, "nested", "alpha"), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	writeFile(t, filepath.Join(repositoryPath, "nested", "alpha", "one.txt"), []byte("one\n"))
	writeFile(t, filepath.Join(repositoryPath, "nested", "two.txt"), []byte("two\n"))

	service := newTestService(repositoryPath)
	snapshot, err := service.GetRepositoryStatus(gitdomain.GetRepositoryStatusRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetRepositoryStatus returned error: %v", err)
	}

	if snapshot.Summary.UntrackedCount != 2 {
		t.Fatalf("expected untracked count 2, got %+v", snapshot.Summary)
	}
	if !hasChangeByPath(snapshot.Changes, "nested/alpha/one.txt") {
		t.Fatalf("expected nested file change, got %+v", snapshot.Changes)
	}
	if !hasChangeByPath(snapshot.Changes, "nested/two.txt") {
		t.Fatalf("expected nested file change, got %+v", snapshot.Changes)
	}
	if hasChangeByPath(snapshot.Changes, "nested") || hasChangeByPath(snapshot.Changes, "nested/") || hasChangeByPath(snapshot.Changes, "nested/alpha") || hasChangeByPath(snapshot.Changes, "nested/alpha/") {
		t.Fatalf("expected untracked directories to be expanded to files, got %+v", snapshot.Changes)
	}
}

func TestGetRepositoryStatusCountsConflicts(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createConflictFixtureRepository(t)
	service := newTestService(repositoryPath)

	snapshot, err := service.GetRepositoryStatus(gitdomain.GetRepositoryStatusRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetRepositoryStatus returned error: %v", err)
	}

	if snapshot.Summary.ConflictedCount != 1 {
		t.Fatalf("expected conflicted count 1, got %+v", snapshot.Summary)
	}

	conflicted := requireChangeByPath(t, snapshot.Changes, "a.txt")
	if !conflicted.IsConflicted {
		t.Fatalf("expected conflicted change, got %+v", conflicted)
	}
	if conflicted.StagedStatus != "unmerged" || conflicted.WorktreeStatus != "unmerged" {
		t.Fatalf("unexpected conflicted statuses: %+v", conflicted)
	}
}

func TestCommitRepositoryChangesCommitsOnlySelectedFiles(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createCommitFixtureRepository(t)
	service := newTestService(repositoryPath)

	result, err := service.CommitRepositoryChanges(gitdomain.CommitRepositoryChangesRequest{
		Message:       "feat: commit selected files",
		RelativePaths: []string{"tracked.txt", "new.txt"},
		RepositoryID:  "repo-1",
	})
	if err != nil {
		t.Fatalf("CommitRepositoryChanges returned error: %v", err)
	}

	if strings.TrimSpace(result.CommitHash) == "" {
		t.Fatalf("expected non-empty commit hash, got %+v", result)
	}
	if result.RepositoryID != "repo-1" {
		t.Fatalf("expected repository id repo-1, got %+v", result)
	}
	if result.Status.Summary.TotalCount != 1 || result.Status.Summary.StagedCount != 1 {
		t.Fatalf("expected only unselected staged file to remain, got %+v", result.Status.Summary)
	}
	if !hasChangeByPath(result.Status.Changes, "keep.txt") {
		t.Fatalf("expected keep.txt to remain changed, got %+v", result.Status.Changes)
	}
	if hasChangeByPath(result.Status.Changes, "tracked.txt") || hasChangeByPath(result.Status.Changes, "new.txt") {
		t.Fatalf("expected selected files to be committed, got %+v", result.Status.Changes)
	}

	if got := strings.TrimSpace(runGitCommand(t, repositoryPath, "log", "-1", "--pretty=%s")); got != "feat: commit selected files" {
		t.Fatalf("unexpected commit message: %q", got)
	}
	if got := strings.TrimSpace(runGitCommand(t, repositoryPath, "status", "--short")); got != "M  keep.txt" {
		t.Fatalf("unexpected git status after commit: %q", got)
	}
	showOutput := runGitCommand(t, repositoryPath, "show", "--name-only", "--pretty=format:%s", "HEAD")
	if !containsAll(showOutput, "feat: commit selected files", "tracked.txt", "new.txt") {
		t.Fatalf("expected selected files in commit output, got %q", showOutput)
	}
	if strings.Contains(showOutput, "keep.txt") {
		t.Fatalf("expected keep.txt to stay out of commit, got %q", showOutput)
	}
}

func TestCommitRepositoryChangesRejectsEmptyMessage(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createCommitFixtureRepository(t)
	service := newTestService(repositoryPath)

	_, err := service.CommitRepositoryChanges(gitdomain.CommitRepositoryChangesRequest{
		Message:       "   ",
		RelativePaths: []string{"tracked.txt"},
		RepositoryID:  "repo-1",
	})
	if err == nil || !strings.Contains(err.Error(), "commit message is required") {
		t.Fatalf("expected empty message error, got %v", err)
	}
}

func TestCommitRepositoryChangesHandlesRenamedFileSelection(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createRenameCommitFixtureRepository(t)
	service := newTestService(repositoryPath)
	// 你好吗？
	result, err := service.CommitRepositoryChanges(gitdomain.CommitRepositoryChangesRequest{
		Message:       "refactor: rename file",
		RelativePaths: []string{"new.txt"},
		RepositoryID:  "repo-1",
	})
	if err != nil {
		t.Fatalf("CommitRepositoryChanges returned error: %v", err)
	}
	if result.Status.HasChanges {
		t.Fatalf("expected repository to be clean after rename commit, got %+v", result.Status)
	}

	showOutput := runGitCommand(t, repositoryPath, "show", "--name-status", "--pretty=format:%s", "HEAD")
	if !containsAll(showOutput, "refactor: rename file", "R100", "old.txt", "new.txt") {
		t.Fatalf("expected rename commit output, got %q", showOutput)
	}
}

func TestPushRepositoryPushesCurrentBranchToUpstream(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.localPath, "push.txt"), []byte("push\n"))
	runGitCommand(t, fixture.localPath, "add", ".")
	runGitCommand(t, fixture.localPath, "commit", "-m", "feat: local push")

	result, err := service.PushRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PushRepository returned error: %v", err)
	}
	if result.Status.AheadCount != 0 || result.Status.BehindCount != 0 {
		t.Fatalf("expected synced status after push, got %+v", result.Status)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.originPath, "log", "-1", "--pretty=%s", "main")); got != "feat: local push" {
		t.Fatalf("expected origin head to match pushed commit, got %q", got)
	}
}

func TestPushRepositoryPublishesCurrentBranchWhenRemoteExistsWithoutUpstream(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath, originPath := createBootstrapPushFixtureRepository(t)
	service := newTestService(repositoryPath)

	result, err := service.PushRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PushRepository returned error: %v", err)
	}
	if result.Status.Upstream != "origin/main" {
		t.Fatalf("expected upstream to be configured after first push, got %+v", result.Status)
	}
	if got := strings.TrimSpace(runGitCommand(t, repositoryPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")); got != "origin/main" {
		t.Fatalf("expected branch upstream to be origin/main, got %q", got)
	}
	if got := strings.TrimSpace(runGitCommand(t, originPath, "log", "-1", "--pretty=%s", "main")); got != "feat: base" {
		t.Fatalf("expected first push to publish local head, got %q", got)
	}
}

func TestPushRepositoryRejectsWhenBranchIsBehind(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.collaboratorPath, "remote.txt"), []byte("remote\n"))
	runGitCommand(t, fixture.collaboratorPath, "add", ".")
	runGitCommand(t, fixture.collaboratorPath, "commit", "-m", "feat: remote ahead")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")
	runGitCommand(t, fixture.localPath, "fetch", "origin")

	_, err := service.PushRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err == nil || !strings.Contains(err.Error(), "pull remote changes before pushing") {
		t.Fatalf("expected behind push error, got %v", err)
	}
}

func TestPullRepositoryFastForwardsFromUpstream(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.collaboratorPath, "pull.txt"), []byte("pull\n"))
	runGitCommand(t, fixture.collaboratorPath, "add", ".")
	runGitCommand(t, fixture.collaboratorPath, "commit", "-m", "feat: remote pull")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")

	result, err := service.PullRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PullRepository returned error: %v", err)
	}
	if result.Status.AheadCount != 0 || result.Status.BehindCount != 0 {
		t.Fatalf("expected synced status after pull, got %+v", result.Status)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.localPath, "log", "-1", "--pretty=%s")); got != "feat: remote pull" {
		t.Fatalf("expected local head to match pulled commit, got %q", got)
	}
}

func TestFetchRepositoryUpdatesBehindCountFromRemote(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.collaboratorPath, "fetch.txt"), []byte("fetch\n"))
	runGitCommand(t, fixture.collaboratorPath, "add", ".")
	runGitCommand(t, fixture.collaboratorPath, "commit", "-m", "feat: remote fetch")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")

	beforeFetch, err := service.GetRepositoryStatus(gitdomain.GetRepositoryStatusRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetRepositoryStatus returned error before fetch: %v", err)
	}
	if beforeFetch.BehindCount != 0 {
		t.Fatalf("expected stale local remote-tracking state before fetch, got %+v", beforeFetch)
	}

	afterFetch, err := service.FetchRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("FetchRepository returned error: %v", err)
	}
	if afterFetch.Status.BehindCount != 1 {
		t.Fatalf("expected behind count after fetch, got %+v", afterFetch.Status)
	}
}

func TestPullRepositoryMergesDivergedBranchWithoutConflicts(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.localPath, "local.txt"), []byte("local\n"))
	runGitCommand(t, fixture.localPath, "add", ".")
	runGitCommand(t, fixture.localPath, "commit", "-m", "feat: local ahead")

	writeFile(t, filepath.Join(fixture.collaboratorPath, "remote.txt"), []byte("remote\n"))
	runGitCommand(t, fixture.collaboratorPath, "add", ".")
	runGitCommand(t, fixture.collaboratorPath, "commit", "-m", "feat: remote ahead")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")

	result, err := service.PullRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PullRepository returned error: %v", err)
	}
	if result.Status.MergeState.InProgress {
		t.Fatalf("expected merge to finish without conflicts, got %+v", result.Status.MergeState)
	}
	if result.Status.BehindCount != 0 {
		t.Fatalf("expected no incoming commits after merge pull, got %+v", result.Status)
	}
	if got := strings.Fields(strings.TrimSpace(runGitCommand(t, fixture.localPath, "rev-list", "--parents", "-n", "1", "HEAD"))); len(got) != 3 {
		t.Fatalf("expected merge commit with 2 parents, got %v", got)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.localPath, "show", "HEAD:local.txt")); got != "local" {
		t.Fatalf("expected merged repository to keep local.txt, got %q", got)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.localPath, "show", "HEAD:remote.txt")); got != "remote" {
		t.Fatalf("expected merged repository to include remote.txt, got %q", got)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.localPath, "status", "--short")); got != "" {
		t.Fatalf("expected clean worktree after merge pull, got %q", got)
	}
}

func TestPullRepositoryIgnoresBrokenNonUpstreamRemote(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)
	brokenRemotePath := filepath.Join(filepath.Dir(fixture.originPath), "missing.git")

	runGitCommand(t, fixture.localPath, "remote", "add", "backup", brokenRemotePath)
	writeFile(t, filepath.Join(fixture.collaboratorPath, "remote.txt"), []byte("remote\n"))
	runGitCommand(t, fixture.collaboratorPath, "add", ".")
	runGitCommand(t, fixture.collaboratorPath, "commit", "-m", "feat: remote ahead")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")

	result, err := service.PullRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PullRepository returned error with broken non-upstream remote: %v", err)
	}
	if result.Status.BehindCount != 0 || result.Status.MergeState.InProgress {
		t.Fatalf("expected pull to succeed via upstream remote only, got %+v", result.Status)
	}
	if got := strings.TrimSpace(runGitCommand(t, fixture.localPath, "show", "HEAD:remote.txt")); got != "remote" {
		t.Fatalf("expected pull to include upstream commit, got %q", got)
	}
}

func TestPullRepositoryReturnsMergeStateWhenConflictsOccur(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	writeFile(t, filepath.Join(fixture.localPath, "base.txt"), []byte("local\n"))
	runGitCommand(t, fixture.localPath, "commit", "-am", "feat: local ahead")

	writeFile(t, filepath.Join(fixture.collaboratorPath, "base.txt"), []byte("remote\n"))
	runGitCommand(t, fixture.collaboratorPath, "commit", "-am", "feat: remote ahead")
	runGitCommand(t, fixture.collaboratorPath, "push", "origin", "main")

	result, err := service.PullRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("PullRepository returned error: %v", err)
	}
	if !result.Status.MergeState.InProgress || !result.Status.MergeState.HasConflicts {
		t.Fatalf("expected merge conflict state after pull, got %+v", result.Status.MergeState)
	}
	if result.Status.MergeState.HeadHash == "" || result.Status.MergeState.HeadShortHash == "" || result.Status.MergeState.HeadSubject == "" {
		t.Fatalf("expected merge metadata to be populated, got %+v", result.Status.MergeState)
	}
	if result.Status.Summary.ConflictedCount != 1 {
		t.Fatalf("expected conflicted count 1, got %+v", result.Status.Summary)
	}
	if !containsAll(runGitCommand(t, fixture.localPath, "status", "--short"), "UU base.txt") {
		t.Fatalf("expected unmerged base.txt after conflicted pull")
	}
	if !containsAll(string(runGitCommand(t, fixture.localPath, "show", ":2:base.txt")), "local") {
		t.Fatalf("expected ours stage to contain local content")
	}
	if !containsAll(string(runGitCommand(t, fixture.localPath, "show", ":3:base.txt")), "remote") {
		t.Fatalf("expected theirs stage to contain remote content")
	}
}

func TestPullRepositoryRejectsDirtyWorktree(t *testing.T) {
	requireGitInstalled(t)

	fixture := createSyncFixtureRepository(t)
	service := newTestService(fixture.localPath)

	appendFile(t, filepath.Join(fixture.localPath, "base.txt"), []byte("dirty\n"))

	_, err := service.PullRepository(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err == nil || !strings.Contains(err.Error(), "commit or stash local changes before pulling") {
		t.Fatalf("expected dirty worktree pull error, got %v", err)
	}
}

func TestGetMergeConflictPreviewReturnsBaseOursTheirsAndResult(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createConflictFixtureRepository(t)
	service := newTestService(repositoryPath)

	preview, err := service.GetMergeConflictPreview(gitdomain.GetMergeConflictPreviewRequest{
		RelativePath: "a.txt",
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetMergeConflictPreview returned error: %v", err)
	}
	if !containsAll(preview.Base.Content, "base") {
		t.Fatalf("expected base side content, got %+v", preview.Base)
	}
	if !containsAll(preview.Ours.Content, "main") {
		t.Fatalf("expected ours side content, got %+v", preview.Ours)
	}
	if !containsAll(preview.Theirs.Content, "feature") {
		t.Fatalf("expected theirs side content, got %+v", preview.Theirs)
	}
	if !containsAll(preview.Result.Content, "<<<<<<<", "=======", ">>>>>>>") {
		t.Fatalf("expected conflicted worktree content, got %+v", preview.Result)
	}
}

func TestApplyMergeConflictResolutionAllowsContinuingMerge(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createConflictFixtureRepository(t)
	service := newTestService(repositoryPath)

	statusAfterResolve, err := service.ApplyMergeConflictResolution(gitdomain.ApplyMergeConflictResolutionRequest{
		Content:      "resolved\n",
		RelativePath: "a.txt",
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("ApplyMergeConflictResolution returned error: %v", err)
	}
	if !statusAfterResolve.MergeState.InProgress || statusAfterResolve.Summary.ConflictedCount != 0 {
		t.Fatalf("expected merge to remain in progress but without conflicts, got %+v", statusAfterResolve)
	}

	result, err := service.ContinueRepositoryMerge(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("ContinueRepositoryMerge returned error: %v", err)
	}
	if result.Status.MergeState.InProgress {
		t.Fatalf("expected merge to be completed, got %+v", result.Status.MergeState)
	}
	if got := string(runGitCommand(t, repositoryPath, "show", "HEAD:a.txt")); !containsAll(got, "resolved") {
		t.Fatalf("expected resolved content to be committed, got %q", got)
	}
}

func TestUseMergeConflictSideStagesChosenVersion(t *testing.T) {
	// 哎礼拜
	requireGitInstalled(t)

	repositoryPath := createConflictFixtureRepository(t)
	service := newTestService(repositoryPath)

	statusAfterResolve, err := service.UseMergeConflictSide(gitdomain.UseMergeConflictSideRequest{
		RelativePath: "a.txt",
		RepositoryID: "repo-1",
		Side:         "theirs",
	})
	if err != nil {
		t.Fatalf("UseMergeConflictSide returned error: %v", err)
	}
	if statusAfterResolve.Summary.ConflictedCount != 0 {
		t.Fatalf("expected conflict to be resolved after choosing theirs, got %+v", statusAfterResolve.Summary)
	}
	if got := string(runGitCommand(t, repositoryPath, "show", ":a.txt")); !containsAll(got, "feature") {
		t.Fatalf("expected index to stage theirs content, got %q", got)
	}
}

func TestAbortRepositoryMergeRestoresPreMergeState(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createConflictFixtureRepository(t)
	service := newTestService(repositoryPath)

	result, err := service.AbortRepositoryMerge(gitdomain.SyncRepositoryRequest{
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("AbortRepositoryMerge returned error: %v", err)
	}
	if result.Status.MergeState.InProgress {
		t.Fatalf("expected merge state to be cleared after abort, got %+v", result.Status.MergeState)
	}
	if result.Status.Summary.ConflictedCount != 0 {
		t.Fatalf("expected no conflicted files after abort, got %+v", result.Status.Summary)
	}
	if got := string(runGitCommand(t, repositoryPath, "show", "HEAD:a.txt")); !containsAll(got, "main") {
		t.Fatalf("expected HEAD to remain on main content after abort, got %q", got)
	}
	if got := string(runGitCommand(t, repositoryPath, "show", ":a.txt")); !containsAll(got, "main") {
		t.Fatalf("expected index to be restored to main content after abort, got %q", got)
	}
}

func TestRepositoryRemoteConfigRoundTrip(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createStatusFixtureRepository(t)
	service := newTestService(repositoryPath)

	firstSnapshot, err := service.UpsertRepositoryRemote(gitdomain.UpsertRepositoryRemoteRequest{
		Name:         "origin",
		RepositoryID: "repo-1",
		URL:          "https://example.com/first.git",
	})
	if err != nil {
		t.Fatalf("UpsertRepositoryRemote returned error: %v", err)
	}
	if len(firstSnapshot.Remotes) != 1 || firstSnapshot.Remotes[0].FetchURL != "https://example.com/first.git" {
		t.Fatalf("expected first remote snapshot to contain configured url, got %+v", firstSnapshot)
	}

	secondSnapshot, err := service.UpsertRepositoryRemote(gitdomain.UpsertRepositoryRemoteRequest{
		Name:         "origin",
		// emmmmm
		RepositoryID: "repo-2",
		URL:          "git@github.com:scoful/testGit.git",
	})
	if err != nil {
		t.Fatalf("UpsertRepositoryRemote update returned error: %v", err)
	}
	if len(secondSnapshot.Remotes) != 1 || secondSnapshot.Remotes[0].FetchURL != "git@github.com:scoful/testGit.git" || secondSnapshot.Remotes[0].PushURL != "git@github.com:scoful/testGit.git" {
		t.Fatalf("expected updated remote snapshot, got %+v", secondSnapshot)
	}

	loadedSnapshot, err := service.GetRepositoryRemotes(gitdomain.GetRepositoryRemotesRequest{RepositoryID: "repo-1"})
	if err != nil {
		t.Fatalf("GetRepositoryRemotes returned error: %v", err)
	}
	if len(loadedSnapshot.Remotes) != 1 || loadedSnapshot.Remotes[0].Name != "origin" {
		t.Fatalf("expected remote round trip to preserve origin, got %+v", loadedSnapshot)
	}
}

func TestGetCommitHistoryReturnsEntries(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)

	history, err := service.GetCommitHistory(gitdomain.GetCommitHistoryRequest{
		Limit:        10,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitHistory returned error: %v", err)
	}

	if len(history.Entries) != 3 {
		t.Fatalf("expected 3 history entries, got %+v", history.Entries)
	}
	if history.Entries[0].Subject != "chore: cleanup" || history.Entries[1].Subject != "refactor: rename legacy" || history.Entries[2].Subject != "feat: initial import" {
		t.Fatalf("unexpected commit order: %+v", history.Entries)
	}
	if history.Entries[0].CommitHash != strings.TrimSpace(runGitCommand(t, repositoryPath, "rev-parse", "HEAD")) {
		t.Fatalf("expected first entry to be HEAD, got %+v", history.Entries[0])
	}
	if history.Entries[1].ShortCommitHash == "" || history.Entries[1].AuthorName != "test" || history.Entries[1].CommittedAt <= 0 {
		t.Fatalf("unexpected history metadata: %+v", history.Entries[1])
	}
	if history.RepositoryID != "repo-1" {
		t.Fatalf("unexpected repository id: %+v", history)
	}
}

func TestGetCommitHistorySupportsPagination(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)

	firstPage, err := service.GetCommitHistory(gitdomain.GetCommitHistoryRequest{
		Limit:        2,
		Page:         1,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitHistory first page returned error: %v", err)
	}
	if len(firstPage.Entries) != 2 {
		t.Fatalf("expected 2 entries on first page, got %+v", firstPage.Entries)
	}
	if firstPage.CurrentPage != 1 || firstPage.PageSize != 2 || firstPage.TotalCount != 3 {
		t.Fatalf("unexpected first page metadata: %+v", firstPage)
	}
	if !firstPage.HasNextPage || firstPage.HasPreviousPage {
		t.Fatalf("unexpected first page navigation metadata: %+v", firstPage)
	}

	secondPage, err := service.GetCommitHistory(gitdomain.GetCommitHistoryRequest{
		Limit:        2,
		Page:         2,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitHistory second page returned error: %v", err)
	}
	if len(secondPage.Entries) != 1 || secondPage.Entries[0].Subject != "feat: initial import" {
		t.Fatalf("unexpected second page entries: %+v", secondPage.Entries)
	}
	if secondPage.CurrentPage != 2 || secondPage.PageSize != 2 || secondPage.TotalCount != 3 {
		t.Fatalf("unexpected second page metadata: %+v", secondPage)
	}
	if secondPage.HasNextPage || !secondPage.HasPreviousPage {
		t.Fatalf("unexpected second page navigation metadata: %+v", secondPage)
	}
}

func TestGetCommitHistoryReturnsParentsAndRefs(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createMergeHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)

	history, err := service.GetCommitHistory(gitdomain.GetCommitHistoryRequest{
		Limit:        10,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitHistory returned error: %v", err)
	}

	mergeEntry := findHistoryEntryBySubject(t, history.Entries, "merge: feature branch")
	if len(mergeEntry.ParentHashes) != 2 {
		t.Fatalf("expected merge commit to have 2 parents, got %+v", mergeEntry)
	}
	if !containsStringValue(mergeEntry.Refs, "HEAD -> main") || !containsStringValue(mergeEntry.Refs, "tag: v1.0.0") {
		t.Fatalf("expected merge refs to include HEAD and tag, got %+v", mergeEntry.Refs)
	}

	featureEntry := findHistoryEntryBySubject(t, history.Entries, "feat: feature branch")
	if !containsStringValue(featureEntry.Refs, "feature") {
		t.Fatalf("expected feature branch ref, got %+v", featureEntry.Refs)
	}
}

func TestGetCommitHistoryReturnsEmptyForRepositoryWithoutCommit(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	service := newTestService(repositoryPath)

	history, err := service.GetCommitHistory(gitdomain.GetCommitHistoryRequest{
		Limit:        10,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitHistory returned error: %v", err)
	}
	if len(history.Entries) != 0 {
		t.Fatalf("expected empty history, got %+v", history.Entries)
	}
}

func TestGetCommitFilesReturnsRenamedAndModifiedEntries(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)
	commitHash := findCommitHashBySubject(t, repositoryPath, "refactor: rename legacy")

	files, err := service.GetCommitFiles(gitdomain.GetCommitFilesRequest{
		CommitHash:   commitHash,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitFiles returned error: %v", err)
	}

	if len(files.Files) != 2 {
		t.Fatalf("expected 2 files in commit, got %+v", files.Files)
	}
	if !hasCommitFile(files.Files, "notes.txt", "", "modified") {
		t.Fatalf("expected notes.txt modified entry, got %+v", files.Files)
	}
	if !hasCommitFile(files.Files, "docs/guide.txt", "legacy.txt", "renamed") {
		t.Fatalf("expected rename entry, got %+v", files.Files)
	}
}

func TestGetCommitFilesDecodesQuotedNonASCIIPaths(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createUnicodeHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)
	commitHash := strings.TrimSpace(runGitCommand(t, repositoryPath, "rev-parse", "HEAD"))

	files, err := service.GetCommitFiles(gitdomain.GetCommitFilesRequest{
		CommitHash:   commitHash,
		RepositoryID: "repo-1",
	})
	if err != nil {
		t.Fatalf("GetCommitFiles returned error: %v", err)
	}
	if len(files.Files) != 1 {
		t.Fatalf("expected 1 file in commit, got %+v", files.Files)
	}
	if files.Files[0].RelativePath != "docs/git-diff-view-偶发错乱分析.md" {
		t.Fatalf("expected decoded unicode path, got %+v", files.Files[0])
	}
}

func TestGetCommitFileDiffSupportsRootAndRenamedCommit(t *testing.T) {
	requireGitInstalled(t)

	repositoryPath := createHistoryFixtureRepository(t)
	service := newTestService(repositoryPath)
	rootCommitHash := findCommitHashBySubject(t, repositoryPath, "feat: initial import")
	renameCommitHash := findCommitHashBySubject(t, repositoryPath, "refactor: rename legacy")

	rootPreview, err := service.GetCommitFileDiff(gitdomain.GetCommitFileDiffRequest{
		CommitHash:   rootCommitHash,
		RelativePath: "notes.txt",
		RepositoryID: "repo-1",
		Status:       "added",
	})
	if err != nil {
		t.Fatalf("GetCommitFileDiff root commit returned error: %v", err)
	}
	if rootPreview.View.Original.Label != "EMPTY" || rootPreview.View.Original.Exists {
		t.Fatalf("expected empty original side for root commit, got %+v", rootPreview.View.Original)
	}
	if !rootPreview.View.Modified.Exists || !containsAll(rootPreview.View.Modified.Content, "line one") {
		t.Fatalf("unexpected root modified side: %+v", rootPreview.View.Modified)
	}
	if !rootPreview.HasPatch || !containsAll(rootPreview.Patch, "diff --git", "notes.txt") {
		t.Fatalf("unexpected root patch: %q", rootPreview.Patch)
	}

	renamePreview, err := service.GetCommitFileDiff(gitdomain.GetCommitFileDiffRequest{
		CommitHash:   renameCommitHash,
		OriginalPath: "legacy.txt",
		RelativePath: "docs/guide.txt",
		RepositoryID: "repo-1",
		Status:       "renamed",
	})
	if err != nil {
		t.Fatalf("GetCommitFileDiff rename commit returned error: %v", err)
	}
	if renamePreview.View.Original.Path != "legacy.txt" || renamePreview.View.Modified.Path != "docs/guide.txt" {
		t.Fatalf("unexpected rename paths: %+v", renamePreview.View)
	}
	if !containsAll(renamePreview.View.Original.Content, "legacy v1") || !containsAll(renamePreview.View.Modified.Content, "legacy v2") {
		t.Fatalf("unexpected rename view content: %+v", renamePreview.View)
	}
	if !containsAll(renamePreview.Patch, "rename from legacy.txt", "rename to docs/guide.txt") {
		t.Fatalf("unexpected rename patch: %q", renamePreview.Patch)
	}
}

func newTestService(repositoryPath string) *Service {
	return NewService(stubWorkspaceStateStore{
		state: &workspace.AppState{
			Workspaces: []workspace.Workspace{{
				ID: "workspace-1",
				Repositories: []workspace.Repository{{
					ID:     "repo-1",
					Name:   "repo",
					Path:   repositoryPath,
					Branch: "main",
				}},
			}},
		},
	}, nil)
}

type syncFixtureRepository struct {
	collaboratorPath string
	localPath        string
	originPath       string
}

func createSyncFixtureRepository(t *testing.T) syncFixtureRepository {
	t.Helper()

	basePath := t.TempDir()
	originPath := filepath.Join(basePath, "origin.git")
	seedPath := filepath.Join(basePath, "seed")
	localPath := filepath.Join(basePath, "local")
	collaboratorPath := filepath.Join(basePath, "collaborator")

	runGitCommand(t, basePath, "init", "--bare", "--initial-branch=main", originPath)
	runGitCommand(t, basePath, "clone", originPath, seedPath)
	runGitCommand(t, seedPath, "config", "user.email", "test@example.com")
	runGitCommand(t, seedPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(seedPath, "base.txt"), []byte("base\n"))
	runGitCommand(t, seedPath, "add", ".")
	runGitCommand(t, seedPath, "commit", "-m", "feat: base")
	runGitCommand(t, seedPath, "push", "-u", "origin", "main")

	runGitCommand(t, basePath, "clone", "--branch", "main", originPath, localPath)
	runGitCommand(t, localPath, "config", "user.email", "test@example.com")
	runGitCommand(t, localPath, "config", "user.name", "test")
	runGitCommand(t, basePath, "clone", "--branch", "main", originPath, collaboratorPath)
	runGitCommand(t, collaboratorPath, "config", "user.email", "test@example.com")
	runGitCommand(t, collaboratorPath, "config", "user.name", "test")

	return syncFixtureRepository{
		collaboratorPath: collaboratorPath,
		localPath:        localPath,
		originPath:       originPath,
	}
}

func createBootstrapPushFixtureRepository(t *testing.T) (string, string) {
	t.Helper()

	basePath := t.TempDir()
	originPath := filepath.Join(basePath, "origin.git")
	repositoryPath := filepath.Join(basePath, "local")

	runGitCommand(t, basePath, "init", "--bare", "--initial-branch=main", originPath)
	runGitCommand(t, basePath, "init", "--initial-branch=main", repositoryPath)
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "base.txt"), []byte("base\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "feat: base")
	runGitCommand(t, repositoryPath, "remote", "add", "origin", originPath)

	return repositoryPath, originPath
}

func createStatusFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "mix.txt"), []byte("one\ntwo\n"))
	writeFile(t, filepath.Join(repositoryPath, "rename.txt"), []byte("rename me\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "init")

	writeFile(t, filepath.Join(repositoryPath, "mix.txt"), []byte("one\ntwo\nthree\n"))
	runGitCommand(t, repositoryPath, "add", "mix.txt")
	if err := os.Rename(
		filepath.Join(repositoryPath, "rename.txt"),
		filepath.Join(repositoryPath, "renamed.txt"),
	); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	runGitCommand(t, repositoryPath, "add", "-A")
	appendFile(t, filepath.Join(repositoryPath, "mix.txt"), []byte("four\n"))
	writeFile(t, filepath.Join(repositoryPath, "new.txt"), []byte("new file\n"))

	return repositoryPath
}

func createCommitFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "tracked.txt"), []byte("tracked-base\n"))
	writeFile(t, filepath.Join(repositoryPath, "keep.txt"), []byte("keep-base\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "init")

	writeFile(t, filepath.Join(repositoryPath, "tracked.txt"), []byte("tracked-base\ntracked-selected\n"))
	writeFile(t, filepath.Join(repositoryPath, "keep.txt"), []byte("keep-base\nkeep-staged\n"))
	runGitCommand(t, repositoryPath, "add", "keep.txt")
	writeFile(t, filepath.Join(repositoryPath, "new.txt"), []byte("new-file\n"))

	return repositoryPath
}

func createRenameCommitFixtureRepository(t *testing.T) string {
	t.Helper()1111

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "old.txt"), []byte("rename-me\n"))
	runGitCommand(t, repositoryPath, "add", "old.txt")
	runGitCommand(t, repositoryPath, "commit", "-m", "init")
	if err := os.Rename(filepath.Join(repositoryPath, "old.txt"), filepath.Join(repositoryPath, "new.txt")); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	runGitCommand(t, repositoryPath, "add", "-A")

	return repositoryPath
}

func createConflictFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "a.txt"), []byte("base\n"))
	runGitCommand(t, repositoryPath, "add", "a.txt")
	runGitCommand(t, repositoryPath, "commit", "-m", "init")

	runGitCommand(t, repositoryPath, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(repositoryPath, "a.txt"), []byte("feature\n"))
	runGitCommand(t, repositoryPath, "commit", "-am", "feature")
	runGitCommand(t, repositoryPath, "checkout", "main")
	writeFile(t, filepath.Join(repositoryPath, "a.txt"), []byte("main\n"))
	runGitCommand(t, repositoryPath, "commit", "-am", "main")
	runGitCommandAllowFailure(t, repositoryPath, "merge", "feature")

	return repositoryPath
}

func createHistoryFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "notes.txt"), []byte("line one\n"))
	writeFile(t, filepath.Join(repositoryPath, "legacy.txt"), []byte("legacy v1\n"))
	writeFile(t, filepath.Join(repositoryPath, "remove.txt"), []byte("remove me\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "feat: initial import")

	writeFile(t, filepath.Join(repositoryPath, "notes.txt"), []byte("line one\nline two\n"))
	if err := os.MkdirAll(filepath.Join(repositoryPath, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll docs failed: %v", err)
	}
	if err := os.Rename(filepath.Join(repositoryPath, "legacy.txt"), filepath.Join(repositoryPath, "docs", "guide.txt")); err != nil {
		t.Fatalf("Rename history fixture failed: %v", err)
	}
	appendFile(t, filepath.Join(repositoryPath, "docs", "guide.txt"), []byte("legacy v2\n"))
	runGitCommand(t, repositoryPath, "add", "-A")
	runGitCommand(t, repositoryPath, "commit", "-m", "refactor: rename legacy")

	if err := os.Remove(filepath.Join(repositoryPath, "remove.txt")); err != nil {
		t.Fatalf("Remove history fixture file failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repositoryPath, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll src failed: %v", err)
	}
	writeFile(t, filepath.Join(repositoryPath, "src", "app.ts"), []byte("export const ready = true;\n"))
	runGitCommand(t, repositoryPath, "add", "-A")
	runGitCommand(t, repositoryPath, "commit", "-m", "chore: cleanup")

	return repositoryPath
}

func createUnicodeHistoryFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	if err := os.MkdirAll(filepath.Join(repositoryPath, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll docs failed: %v", err)
	}
	writeFile(t, filepath.Join(repositoryPath, "docs", "git-diff-view-偶发错乱分析.md"), []byte("hello\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "docs: add unicode file")

	return repositoryPath
}

func createMergeHistoryFixtureRepository(t *testing.T) string {
	t.Helper()

	repositoryPath := t.TempDir()
	runGitCommand(t, repositoryPath, "init", "--initial-branch=main")
	runGitCommand(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitCommand(t, repositoryPath, "config", "user.name", "test")
	writeFile(t, filepath.Join(repositoryPath, "base.txt"), []byte("base\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "feat: base")

	runGitCommand(t, repositoryPath, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(repositoryPath, "feature.txt"), []byte("feature\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "feat: feature branch")

	runGitCommand(t, repositoryPath, "checkout", "main")
	writeFile(t, filepath.Join(repositoryPath, "main.txt"), []byte("main\n"))
	runGitCommand(t, repositoryPath, "add", ".")
	runGitCommand(t, repositoryPath, "commit", "-m", "fix: main branch")
	runGitCommand(t, repositoryPath, "merge", "--no-ff", "feature", "-m", "merge: feature branch")
	runGitCommand(t, repositoryPath, "tag", "v1.0.0")

	return repositoryPath
}

func findCommitHashBySubject(t *testing.T, repositoryPath string, subject string) string {
	t.Helper()

	output := runGitCommand(t, repositoryPath, "log", "--pretty=format:%H%x09%s")
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(fields) == 2 && fields[1] == subject {
			return fields[0]
		}
	}

	t.Fatalf("commit subject not found: %s", subject)
	return ""
}

func findHistoryEntryBySubject(t *testing.T, entries []gitdomain.RepositoryCommitHistoryEntry, subject string) gitdomain.RepositoryCommitHistoryEntry {
	t.Helper()

	for _, entry := range entries {
		if entry.Subject == subject {
			return entry
		}
	}

	t.Fatalf("history subject not found: %s", subject)
	return gitdomain.RepositoryCommitHistoryEntry{}
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func hasCommitFile(files []gitdomain.RepositoryCommitFile, relativePath string, originalPath string, status string) bool {
	for _, file := range files {
		if file.RelativePath == relativePath && file.OriginalPath == originalPath && file.Status == status {
			return true
		}
	}

	return false
}

func requireGitInstalled(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git executable is unavailable: %v", err)
	}
}

func runGitCommand(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
	return string(output)
}

func runGitCommandAllowFailure(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("git %v failed unexpectedly: %v\n%s", args, err, string(output))
	}

	return string(output)
}

func requireChangeByPath(t *testing.T, changes []gitdomain.RepositoryChange, relativePath string) gitdomain.RepositoryChange {
	t.Helper()
	for _, change := range changes {
		if change.RelativePath == relativePath {
			return change
		}
	}
	t.Fatalf("change not found: %s", relativePath)
	return gitdomain.RepositoryChange{}
}

func hasChangeByPath(changes []gitdomain.RepositoryChange, relativePath string) bool {
	for _, change := range changes {
		if change.RelativePath == relativePath {
			return true
		}
	}

	return false
}

func writeFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("WriteFile failed for %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile failed for %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		t.Fatalf("Append failed for %s: %v", path, err)
	}
}

func containsAll(value string, fragments ...string) bool {

	return true
}
