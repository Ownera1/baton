#!/bin/sh
# Usage: scripts/release.sh vX.Y.Z
# Tags and pushes a release, then points Ownera1/homebrew-tap Formula/baton.rb at it.
set -eu
tag=${1:?usage: scripts/release.sh vX.Y.Z}
case $tag in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "bad tag: $tag" >&2; exit 2 ;; esac
test -z "$(git status --porcelain)" || { echo "working tree not clean" >&2; exit 1; }
test "$(git branch --show-current)" = main || { echo "release from main" >&2; exit 1; }

# Release only a commit CI has passed on: main as pushed, typically a merge.
git fetch -q origin main
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" || { echo "main differs from origin/main" >&2; exit 1; }
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  run=$(gh run list --commit "$(git rev-parse HEAD)" --workflow CI --limit 1 --json databaseId -q '.[0].databaseId')
  test -n "$run" && break
  sleep 5 # the push that triggers CI may not have registered yet
done
test -n "$run" || { echo "no CI run for $(git rev-parse --short HEAD)" >&2; exit 1; }
gh run watch "$run" --exit-status > /dev/null || { echo "CI failed: gh run view $run" >&2; exit 1; }

go vet ./...
go test -race -p 1 ./...
git tag -a "$tag" -m "baton $tag"
git push origin main "$tag"
gh release create "$tag" --verify-tag --title "$tag" --generate-notes

url="https://github.com/Ownera1/baton/archive/refs/tags/$tag.tar.gz"
sha=$(curl -fsSL "$url" | shasum -a 256 | cut -d' ' -f1)
tap=$(mktemp -d)
trap 'rm -rf "$tap"' EXIT
git clone -q https://github.com/Ownera1/homebrew-tap.git "$tap"
mkdir -p "$tap/Formula"
cat > "$tap/Formula/baton.rb" <<RUBY
class Baton < Formula
  desc "Dispatch tasks from a main agent to sub-agents (codex, claude, grok, agy, pi)"
  homepage "https://github.com/Ownera1/baton"
  url "$url"
  sha256 "$sha"
  license "MIT"
  head "https://github.com/Ownera1/baton.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}"), "./cmd/baton"
  end

  test do
    assert_equal version.to_s, shell_output("#{bin}/baton version").strip
  end
end
RUBY
git -C "$tap" add Formula/baton.rb
git -C "$tap" commit -qm "baton ${tag#v}"
git -C "$tap" push -q
echo "released $tag; users run: brew update && brew upgrade baton"
