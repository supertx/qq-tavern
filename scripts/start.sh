#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
[ "$#" -le 2 ] || { printf '参数过多\n' >&2; exit 2; }
mode=${1:-}
if [ -z "$mode" ]; then
    printf 'QQ 连接方式：1) 扫码  2) 手动 AppID/AppSecret\n请选择 [1/2]: '
    read -r choice
    case "$choice" in 1) mode=qr ;; 2) mode=manual ;; *) exit 2 ;; esac
fi
rebind=${2:-}
case "$mode:$rebind" in
    manual:|qr:|qr:--rebind) ;;
    *) printf '用法：%s [manual|qr [--rebind]]\n' "$0" >&2; exit 2 ;;
esac
if [ ! -f .env ]; then
    printf '请先 cp .env.example .env，并填写模型接口和 INTERNAL_API_TOKEN。\n' >&2
    exit 1
fi
set -- -f compose.yaml
if [ "$mode" = qr ]; then set -- "$@" -f compose.qr.yaml; fi
docker compose "$@" config --quiet
if [ "$mode" = qr ]; then
    # Stop both consumers before writing a new shared bot identity.
    docker compose "$@" stop gateway worker
    if [ "$rebind" = --rebind ]; then
        docker compose "$@" run --rm --no-deps --build -e QQ_LOGIN_FORCE=true qq-login
    else
        docker compose "$@" run --rm --no-deps --build qq-login
    fi
fi
# Fail before bringing up a partially configured stack.
docker compose "$@" run --rm --no-deps --build -e ROLE=gateway gateway check
docker compose "$@" run --rm --no-deps -e ROLE=worker worker check
docker compose "$@" up -d --build redis gateway worker
docker compose "$@" ps
