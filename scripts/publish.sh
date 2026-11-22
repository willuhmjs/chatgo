#!/usr/bin/env bash
# Publish the current HEAD to the PUBLIC GitHub repo with a clean single-commit
# history. The local repo keeps internal docs + full history; this ships only
# tracked, non-excluded files under the noreply identity.
#
#   scripts/publish.sh v0.5.0 "release notes subject"
#
# What it does: git archive HEAD -> temp dir -> strip internal docs -> leak
# sweep (hard-fails on any personal token) -> fresh git init -> commit+tag ->
# force-push to git@github.com:willuhmjs/chatgo.git.
set -euo pipefail
VERSION="${1:?usage: publish.sh vX.Y.Z [subject]}"
SUBJECT="${2:-ChatGo $VERSION}"
SRC="$(git rev-parse --show-toplevel)"
OUT="$(mktemp -d /tmp/chatgo-pub.XXXX)"

git -C "$SRC" archive HEAD | tar -x -C "$OUT"
cd "$OUT"

# Internal working docs — never public.
rm -f docs/INTEGRATION-TODO.md docs/PUB-*.md docs/SECREV*.md \
      docs/SECURITY-REVIEW*.md docs/WAVE*-PLAN.md scripts/publish.sh

# Hard-fail leak gate: any hit means STOP and fix the tree first.
if grep -rniE "will\.net|willuhmjs|gmail|/Users/will|Faircloth|od\.edu|SESSION_SECRET=[a-f0-9]" . \
    --exclude-dir=.git 2>/dev/null; then
  echo "LEAK SWEEP FAILED — fix the hits above, nothing pushed" >&2
  exit 1
fi

git init -q -b main .
git add -A
git -c user.name="willuhmjs" -c user.email="willuhmjs@users.noreply.github.com" \
    commit -q -m "$SUBJECT"
git tag -a "$VERSION" -m "release $VERSION"
git remote add origin https://github.com/willuhmjs/chatgo.git
git push -q --force --tags origin main
echo "published $VERSION -> https://github.com/willuhmjs/chatgo (tree: $OUT)"
