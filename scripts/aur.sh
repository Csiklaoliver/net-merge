#!/usr/bin/env bash
# Updates and builds the AUR packages (net-merge from source, net-merge-bin from the release
# tarballs) for one version. Runs in an archlinux container with this repository mounted at /src:
#
#   URL=http://localhost:8001 scripts/aur.sh test 0.0.0     CI: build both from local files laid out like GitHub's
#   AUR_SSH_PRIVATE_KEY=... scripts/aur.sh publish 0.3.0    release: push both to aur.archlinux.org
set -euo pipefail
mode=${1:?usage: aur.sh test|publish VERSION}
v=${2:?usage: aur.sh test|publish VERSION}

if [ "$(id -u)" = 0 ]; then   # makepkg refuses root: install the tools, then carry on as a normal user
    pacman -Syu --noconfirm --needed base-devel git go openssh >/dev/null
    id builder >/dev/null 2>&1 || useradd -m builder
    if [ "$mode" = publish ]; then
        : "${AUR_SSH_PRIVATE_KEY:?set AUR_SSH_PRIVATE_KEY}"
        install -d -m700 -o builder /home/builder/.ssh
        printf '%s\n' "$AUR_SSH_PRIVATE_KEY" > /home/builder/.ssh/id_ed25519
        chown builder /home/builder/.ssh/id_ed25519
        chmod 600 /home/builder/.ssh/id_ed25519
    fi
    exec runuser -u builder -- env HOME=/home/builder URL="${URL:-}" bash "$0" "$@"
fi

url=${URL:-https://github.com/rohit267/net-merge}
work=$HOME/aur; mkdir -p "$work"
export GIT_SSH_COMMAND="ssh -i $HOME/.ssh/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
git config --global user.name "net-merge release"
git config --global user.email "rohit@champdev.me"

# pacman arch : name in the release tarball
declare -A tarball=([x86_64]=x86_64 [aarch64]=aarch64 [armv7h]=armv7 [armv6h]=armv6 [i686]=i686
                    [riscv64]=riscv64 [powerpc64le]=ppc64le [s390x]=s390x [loong64]=loongarch64)
sum() { curl -fsSL "$1" | sha256sum | cut -d' ' -f1; }

fill() {   # fill DIR: version and checksums of the files that version published
    local d=$1 a
    sed -i "s|^pkgver=.*|pkgver=$v|; s|^pkgrel=.*|pkgrel=1|" "$d/PKGBUILD"
    [ -z "${URL:-}" ] || sed -i "s|^url=.*|url='$url'|" "$d/PKGBUILD"
    sed -i "s|^sha256sums=.*|sha256sums=('$(sum "$url/archive/v$v.tar.gz")')|" "$d/PKGBUILD"
    if [ "$(basename "$d")" = net-merge-bin ]; then
        for a in "${!tarball[@]}"; do
            sed -i "s|^sha256sums_$a=.*|sha256sums_$a=('$(sum "$url/releases/download/v$v/net-merge-$v-linux-${tarball[$a]}.tar.gz")')|" "$d/PKGBUILD"
        done
    fi
    (cd "$d" && makepkg --printsrcinfo > .SRCINFO)
}

for pkg in net-merge net-merge-bin; do
    d=$work/$pkg
    if [ "$mode" = publish ]; then
        git -c init.defaultBranch=master clone "ssh://aur@aur.archlinux.org/$pkg.git" "$d"
    else
        mkdir -p "$d"
    fi
    cp /src/packaging/aur/$pkg/PKGBUILD /src/packaging/aur/net-merge.install "$d/"
    fill "$d"
    cd "$d"
    if [ "$mode" = publish ]; then
        git add -A
        if git diff --cached --quiet; then echo "$pkg $v is already on the AUR"; else
            git commit -qm "Update to $v"
            git push origin HEAD:master
        fi
    else
        makepkg -f --noconfirm
        p=$(ls ./*.pkg.tar.zst)
        bsdtar -tf "$p" | grep -q usr/bin/net-merge
        bsdtar -xOf "$p" usr/bin/net-merge > "$work/nm-$pkg" && chmod +x "$work/nm-$pkg"
        "$work/nm-$pkg" version
        echo "== $pkg built and runs"
    fi
done
