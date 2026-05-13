#!/usr/bin/env bash
# Локальная сборка всех Docker-образов сервисов.
# Использует тот же Dockerfile с SERVICE arg, что и CI.
#
#   ./scripts/build-images.sh              — собрать все
#   ./scripts/build-images.sh audit        — собрать один
#   TAG=v0.1 ./scripts/build-images.sh     — задать тег
set -euo pipefail

SERVICES=(
  tender-intel
  tender-intel-worker
  audit
  document
  submission
  submission-worker
  esign
  console-bff
  identity
)

TAG="${TAG:-dev}"
PREFIX="${PREFIX:-goszakup}"

selected=("$@")
if [ ${#selected[@]} -eq 0 ]; then
  selected=("${SERVICES[@]}")
fi

for svc in "${selected[@]}"; do
  echo ">> building ${PREFIX}/${svc}:${TAG}"
  docker build \
    --build-arg "SERVICE=${svc}" \
    -t "${PREFIX}/${svc}:${TAG}" \
    .
done

echo "Done. Built ${#selected[@]} image(s)."
