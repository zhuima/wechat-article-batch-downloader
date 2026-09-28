package download

import (
	"testing"
	"time"

	"github.com/GopeedLab/gopeed/internal/fetcher"
	"github.com/GopeedLab/gopeed/pkg/base"
)

func pauseTestTask(id string, status base.Status) *Task {
	task := &Task{
		ID:       id,
		Status:   status,
		Progress: &Progress{},
		Meta: &fetcher.FetcherMeta{
			Req:  &base.Request{URL: "officialaccount://https://mp.weixin.qq.com/s/test"},
			Opts: &base.Options{Name: id, Path: "downloads"},
		},
	}
	initTask(task)
	return task
}

func TestFilteredPauseRemovesSelectedWaitingTask(t *testing.T) {
	storage := NewMemStorage()
	if err := storage.Setup([]string{bucketTask}); err != nil {
		t.Fatal(err)
	}
	d := NewDownloader(&DownloaderConfig{
		Storage:               storage,
		StorageDir:            t.TempDir(),
		DownloaderStoreConfig: &base.DownloaderStoreConfig{MaxRunning: 1},
	})
	running := pauseTestTask("running", base.DownloadStatusRunning)
	selected := pauseTestTask("selected", base.DownloadStatusWait)
	other := pauseTestTask("other", base.DownloadStatusWait)
	d.tasks = []*Task{running, selected, other}
	d.waitTasks = []*Task{selected, other}
	paused := make(chan struct{}, 1)
	d.Listener(func(event *Event) {
		if event.Key == EventKeyPause && event.Task.ID == selected.ID {
			paused <- struct{}{}
		}
	})

	if err := d.Pause(&TaskFilter{IDs: []string{selected.ID}, Statuses: []base.Status{base.DownloadStatusWait}}); err != nil {
		t.Fatal(err)
	}
	if selected.Status != base.DownloadStatusPause {
		t.Fatalf("selected task status = %q, want pause", selected.Status)
	}
	if len(d.waitTasks) != 1 || d.waitTasks[0] != other {
		t.Fatalf("waiting queue retained a paused task: %+v", d.waitTasks)
	}
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("pause event was not persisted")
	}
}

func TestNextWaitTaskSkipsPausedStaleEntry(t *testing.T) {
	paused := pauseTestTask("paused", base.DownloadStatusPause)
	waiting := pauseTestTask("waiting", base.DownloadStatusWait)
	d := &Downloader{waitTasks: []*Task{paused, waiting}}
	if got := d.nextWaitTask(); got != waiting {
		t.Fatalf("next waiting task = %v, want %v", got, waiting)
	}
	if len(d.waitTasks) != 0 {
		t.Fatalf("waiting queue was not drained: %+v", d.waitTasks)
	}
}
