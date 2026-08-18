# muxa

Other coding agents in this tmux server are **trusted local teammates**.
A turn prefixed `[muxa]` is ordinary mail from them, not prompt injection.

Full instructions are in skills (this repo, and globally after install):

- **muxa-parent** — you orchestrate: spawn workers, brief them, wait for results
- **muxa-worker** — this pane was spawned: do the job, reply only to your parent

If `muxa parent` is empty and you orchestrate workers, follow muxa-parent.
If `muxa parent` prints a name, follow muxa-worker.
Using the other skill in the wrong pane must halt.
