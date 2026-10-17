#!/usr/bin/env bash
# Builds a signed apt repository and a signed dnf repository from .deb/.rpm files, as a static
# site that can be served from GitHub Pages. Needs apt-ftparchive (apt-utils), createrepo_c and
# gpg, with the signing key already in the keyring.
#
#   GPG_KEY_ID=<fingerprint> REPO_BASE_URL=https://rohit267.github.io/net-merge \
#       scripts/build-repos.sh PKG_DIR OUT_DIR
set -euo pipefail
PKGS=$(realpath "${1:?usage: build-repos.sh PKG_DIR OUT_DIR}")
mkdir -p "${2:?usage: build-repos.sh PKG_DIR OUT_DIR}"
OUT=$(realpath "$2")
KEY=${GPG_KEY_ID:?set GPG_KEY_ID to the signing key fingerprint}
BASE=${REPO_BASE_URL:-https://rohit267.github.io/net-merge}
sign() { gpg --batch --yes --pinentry-mode loopback --local-user "$KEY" "$@"; }

rm -rf "$OUT"/*
mkdir -p "$OUT/apt/pool/main" "$OUT/rpm"
cp "$PKGS"/*.deb "$OUT/apt/pool/main/"
cp "$PKGS"/*.rpm "$OUT/rpm/"

# ---- apt: dists/stable/main/binary-<arch>/Packages, Release, InRelease
cd "$OUT/apt"
for arch in amd64 arm64; do
    d=dists/stable/main/binary-$arch
    mkdir -p "$d"
    apt-ftparchive --arch "$arch" packages pool > "$d/Packages"
    gzip -9kf "$d/Packages"
done
apt-ftparchive \
    -o APT::FTPArchive::Release::Origin=net-merge \
    -o APT::FTPArchive::Release::Label=net-merge \
    -o APT::FTPArchive::Release::Suite=stable \
    -o APT::FTPArchive::Release::Codename=stable \
    -o APT::FTPArchive::Release::Architectures="amd64 arm64" \
    -o APT::FTPArchive::Release::Components=main \
    -o APT::FTPArchive::Release::Description="net-merge: use all your network adapters at once" \
    release dists/stable > dists/stable/Release
sign --clearsign -o dists/stable/InRelease dists/stable/Release
sign --armor --detach-sign -o dists/stable/Release.gpg dists/stable/Release
gpg --export "$KEY" > key.gpg      # binary keyring, for signed-by=
gpg --armor --export "$KEY" > key.asc

# ---- dnf: repodata signed with repo_gpgcheck
cd "$OUT/rpm"
createrepo_c --quiet .
sign --armor --detach-sign -o repodata/repomd.xml.asc repodata/repomd.xml
gpg --armor --export "$KEY" > key.asc
cat > net-merge.repo <<REPO
[net-merge]
name=net-merge
baseurl=$BASE/rpm
enabled=1
gpgcheck=0
repo_gpgcheck=1
gpgkey=$BASE/rpm/key.asc
REPO

# ---- a plain page at the root, so the site has something to show
cat > "$OUT/index.html" <<HTML
<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>net-merge packages</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem}pre{background:#f3f4f6;padding:.8rem;overflow:auto;border-radius:6px}@media(prefers-color-scheme:dark){body{background:#0b1220;color:#e5e7eb}pre{background:#111827}a{color:#4ade80}}</style>
<h1>net-merge packages</h1>
<p>Use all your internet connections at once. <a href="https://github.com/rohit267/net-merge">Project on GitHub</a>.</p>
<h2>Debian, Ubuntu, Mint, Pop!_OS</h2>
<pre>curl -fsSL $BASE/apt/key.gpg | sudo tee /usr/share/keyrings/net-merge.gpg &gt;/dev/null
echo "deb [signed-by=/usr/share/keyrings/net-merge.gpg] $BASE/apt stable main" | sudo tee /etc/apt/sources.list.d/net-merge.list
sudo apt update &amp;&amp; sudo apt install net-merge</pre>
<h2>Fedora and RHEL-family</h2>
<pre>sudo curl -fsSL -o /etc/yum.repos.d/net-merge.repo $BASE/rpm/net-merge.repo
sudo dnf install net-merge</pre>
HTML
touch "$OUT/.nojekyll"
echo "built apt and dnf repositories in $OUT"
