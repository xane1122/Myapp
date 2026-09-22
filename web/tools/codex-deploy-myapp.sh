#!/bin/bash
set -euo pipefail

workspace=/root/myapp-workspace/current
app_dir=/opt/myapp
build_dir=/opt/myapp/.codex-deploy
go_bin=/root/.local/go/bin/go

lock_dir=/opt/myapp/.codex-deploy/lock
if ! mkdir "$lock_dir" 2>/dev/null; then
  if [[ -d "$lock_dir" ]]; then
    echo "已有部署正在进行，请稍后重试。" >&2
  else
    echo "无法创建部署锁目录。" >&2
  fi
  exit 1
fi
trap 'rmdir "$lock_dir"' EXIT

install -d -o openrouter -g openrouter -m 0750 "$build_dir"
runuser -u openrouter -- env \
  HOME=/opt/myapp \
  PATH=/root/.local/go/bin:/usr/local/bin:/usr/bin:/bin \
  GOCACHE=/opt/myapp/.cache/go-build \
  /root/.local/go/bin/go clean -cache
runuser -u openrouter -- bash -c 'cd /root/myapp-workspace/current && exec env \
  HOME=/opt/myapp \
  PATH=/root/.local/go/bin:/usr/local/bin:/usr/bin:/bin \
  GOCACHE=/opt/myapp/.cache/go-build \
  GOMODCACHE=/opt/myapp/.cache/go-mod \
  GOPATH=/opt/myapp/.cache/go-path \
  /root/.local/go/bin/go build -tags sqlite_fts5 -o /opt/myapp/.codex-deploy/myapp.new ./cmd/server'

install -m 0755 "$build_dir/myapp.new" "$app_dir/myapp.new"
rsync -a --delete --exclude='.*.swp' "$workspace/static/" "$app_dir/static/"
rsync -a "$workspace/config/" "$app_dir/config/"
mv -f "$app_dir/myapp.new" "$app_dir/myapp"
systemctl restart myapp

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS http://127.0.0.1:5000/api/health >/dev/null; then
    echo "部署完成：myapp 已重启，健康检查通过。"
    exit 0
  fi
  sleep 1
done

systemctl status myapp --no-pager -l >&2
exit 1
