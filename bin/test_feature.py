"""Self-check for bin/feature on a scratch repo with an origin: `python3 bin/test_feature.py`."""
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
tmp = tempfile.mkdtemp()
env = dict(os.environ, FEATURE_ROOT=os.path.join(tmp, "wt"), GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@t", GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@t")


def sh(*a, cwd=None, ok=True):
    """Run a command; assert it succeeded (or failed, with ok=False) and return stdout + stderr."""
    r = subprocess.run(a, cwd=cwd, env=env, capture_output=True, text=True)
    assert (r.returncode == 0) == ok, (a, r.returncode, r.stdout, r.stderr)
    return r.stdout + r.stderr


def feature(*a, ok=True):
    """Run bin/feature with these arguments."""
    return sh(sys.executable, os.path.join(HERE, "feature"), *a, ok=ok)


origin, repo = os.path.join(tmp, "origin.git"), os.path.join(tmp, "repo")
sh("git", "init", "-q", "--bare", origin)
sh("git", "clone", "-q", origin, repo)
open(os.path.join(repo, "a.txt"), "w").write("a\n")
sh("git", "-C", repo, "add", ".")
sh("git", "-C", repo, "commit", "-qm", "init")
sh("git", "-C", repo, "push", "-q", "origin", "HEAD:main", "HEAD:stage")

out = feature("start", repo, "export-v2", "implementer", "tester")
assert "branch feature/export-v2 (from origin/stage)" in out, out
impl, feat = os.path.join(tmp, "wt/export-v2/implementer"), os.path.join(tmp, "wt/export-v2/feature")
assert os.path.isdir(impl) and os.path.isdir(feat)
open(os.path.join(impl, "b.txt"), "w").write("b\n")
assert "uncommitted" in feature("merge", repo, "export-v2", "implementer", ok=False)
sh("git", "-C", impl, "add", ".")
sh("git", "-C", impl, "commit", "-qm", "wip 1")
open(os.path.join(impl, "b.txt"), "a").write("b2\n")
sh("git", "-C", impl, "commit", "-qam", "wip 2")
assert "merged implementer into feature/export-v2" in feature("merge", repo, "export-v2", "implementer")
assert os.path.exists(os.path.join(feat, "b.txt"))
assert "up to date" in feature("sync", repo, "export-v2", "tester")
assert "1 commits ahead" in feature("status", repo, "export-v2") or "commits ahead" in feature("status", repo, "export-v2")

msg = os.path.join(tmp, "msg.md")
open(msg, "w").write("Add the b file\n\n## What changed\nNothing yet\n")
assert "owner's yes" in feature("ship", repo, "export-v2", msg, ok=False)
assert "missing: Why, How it was tested, Technical notes" in feature("ship", repo, "export-v2", msg, "--owner-approved", ok=False)
open(msg, "w").write("Add b to the repo\n\n## What changed\nThere is a b file now.\n\n## Why\nThe owner asked for b.\n\n"
                      "## How it was tested\nRead it back: two lines.\n\n## Technical notes\n")
assert "'Technical notes' is empty" in feature("ship", repo, "export-v2", msg, "--owner-approved", ok=False)
open(msg, "a").write("Only b.txt; nothing to migrate.\n")
out = feature("ship", repo, "export-v2", msg, "--owner-approved")
assert "shipped feature/export-v2 to stage as one commit" in out, out
log = sh("git", "-C", origin, "log", "--format=%s|%b", "stage", "-2")
assert log.startswith("Add b to the repo|## What changed") and "Thread: export-v2" in log and "wip" not in log, log
assert sh("git", "-C", origin, "rev-list", "--count", "stage").strip() == "2"  # init + one squashed commit
assert not os.path.exists(os.path.join(tmp, "wt/export-v2"))
assert "feature/export-v2" not in sh("git", "-C", repo, "branch", "--list")
assert "main" in sh("git", "-C", origin, "branch") and sh("git", "-C", origin, "rev-list", "--count", "main").strip() == "1"
print("ok")
