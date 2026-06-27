package services

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chmouel/lazyworktree/internal/config"
	"github.com/fsnotify/fsnotify"
)

// GitWatchDebounce is the default debounce window for watcher events. It can
// be overridden per-instance from config (git_watch_debounce_ms). Raising it
// throttles how often a filesystem change under .git triggers a full worktree
// refresh — that refresh runs git and can contend for a worktree's index.lock
// with a concurrently-running agent, so a large window keeps the watcher out
// of the agent's way.
const GitWatchDebounce = 600 * time.Millisecond

// GitCommonDirResolver resolves the git common directory for a repository.
type GitCommonDirResolver interface {
	RunGit(ctx context.Context, args []string, cwd string, okReturncodes []int, strip, silent bool) string
}

// GitWatchService manages git watcher state.
type GitWatchService struct {
	Started     bool
	Waiting     bool
	CommonDir   string
	Roots       []string
	Events      chan struct{}
	Done        chan struct{}
	Paths       map[string]struct{}
	Mu          sync.Mutex
	Watcher     *fsnotify.Watcher
	LastRefresh time.Time
	Debounce    time.Duration
	git         GitCommonDirResolver
	logf        func(string, ...any)
}

// NewGitWatchService creates a new GitWatchService.
func NewGitWatchService(git GitCommonDirResolver, logf func(string, ...any)) *GitWatchService {
	return &GitWatchService{
		git:      git,
		logf:     logf,
		Debounce: GitWatchDebounce,
	}
}

// Start initialises the watcher and starts the background goroutine.
func (w *GitWatchService) Start(ctx context.Context, cfg *config.AppConfig) (bool, error) {
	if w.Started || cfg == nil || !cfg.AutoRefresh {
		return false, nil
	}
	commonDir := w.resolveGitCommonDir(ctx)
	if commonDir == "" {
		w.debugf("auto refresh: unable to resolve git common dir")
		return false, nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return false, err
	}

	w.Started = true
	w.Watcher = watcher
	if cfg.GitWatchDebounceMs > 0 {
		w.Debounce = time.Duration(cfg.GitWatchDebounceMs) * time.Millisecond
	}
	w.CommonDir = commonDir
	w.Events = make(chan struct{}, 1)
	w.Done = make(chan struct{})
	w.Paths = make(map[string]struct{})
	w.Roots = []string{
		filepath.Join(commonDir, "refs"),
		filepath.Join(commonDir, "logs"),
		filepath.Join(commonDir, "worktrees"),
	}
	w.addWatchDir(commonDir)
	for _, root := range w.Roots {
		w.addWatchTree(root)
	}

	go w.run()
	return true, nil
}

// Stop stops the watcher and closes channels.
func (w *GitWatchService) Stop() {
	if !w.Started {
		return
	}
	close(w.Done)
	w.Started = false
	if w.Watcher != nil {
		_ = w.Watcher.Close()
	}
}

// NextEvent returns the event channel if waiting is not already active.
func (w *GitWatchService) NextEvent() <-chan struct{} {
	if w.Events == nil || w.Waiting {
		return nil
	}
	w.Waiting = true
	return w.Events
}

// ResetWaiting clears the waiting flag after an event is processed.
func (w *GitWatchService) ResetWaiting() {
	w.Waiting = false
}

// ShouldRefresh checks debounce timing for watcher events.
func (w *GitWatchService) ShouldRefresh(now time.Time) bool {
	debounce := w.Debounce
	if debounce <= 0 {
		debounce = GitWatchDebounce
	}
	if !w.LastRefresh.IsZero() && now.Sub(w.LastRefresh) < debounce {
		return false
	}
	w.LastRefresh = now
	return true
}

// MaybeWatchNewDir registers newly created directories under watch roots.
func (w *GitWatchService) MaybeWatchNewDir(path string) {
	if !w.IsUnderRoot(path) {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}
	w.addWatchDir(path)
}

// Signal notifies listeners of watcher activity.
func (w *GitWatchService) Signal() {
	select {
	case <-w.Done:
		return
	default:
	}
	select {
	case w.Events <- struct{}{}:
	default:
	}
}

// IsUnderRoot reports whether the path is under any watch root.
func (w *GitWatchService) IsUnderRoot(path string) bool {
	if path == "" {
		return false
	}
	for _, root := range w.Roots {
		if root == "" {
			continue
		}
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *GitWatchService) run() {
	for {
		select {
		case <-w.Done:
			return
		case event, ok := <-w.Watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				w.MaybeWatchNewDir(event.Name)
			}
			w.Signal()
		case err, ok := <-w.Watcher.Errors:
			if !ok {
				return
			}
			w.debugf("git watcher error: %v", err)
		}
	}
}

func (w *GitWatchService) addWatchDir(path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	w.Mu.Lock()
	defer w.Mu.Unlock()

	if _, ok := w.Paths[path]; ok {
		return
	}
	if err := w.Watcher.Add(path); err != nil {
		w.debugf("git watcher add failed for %s: %v", path, err)
		return
	}
	w.Paths[path] = struct{}{}
}

func (w *GitWatchService) addWatchTree(root string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		w.addWatchDir(path)
		return nil
	})
}

func (w *GitWatchService) resolveGitCommonDir(ctx context.Context) string {
	if w.git == nil {
		return ""
	}
	commonDir := strings.TrimSpace(w.git.RunGit(ctx, []string{"git", "rev-parse", "--git-common-dir"}, "", []int{0}, true, false))
	if commonDir == "" {
		return ""
	}
	if filepath.IsAbs(commonDir) {
		return commonDir
	}

	repoRoot := strings.TrimSpace(w.git.RunGit(ctx, []string{"git", "rev-parse", "--show-toplevel"}, "", []int{0}, true, false))
	if repoRoot != "" {
		return filepath.Join(repoRoot, commonDir)
	}
	if abs, err := filepath.Abs(commonDir); err == nil {
		return abs
	}
	return commonDir
}

func (w *GitWatchService) debugf(format string, args ...any) {
	if w.logf == nil {
		return
	}
	w.logf(format, args...)
}
