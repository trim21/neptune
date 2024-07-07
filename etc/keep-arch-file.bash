#!/usr/bin/env bash

arch=$(dpkg --print-architecture)

if [[ $arch == "amd64" ]]; then
  mv /dist/tyr_linux_amd64 /dist/tyr
elif [[ $arch == "arm64" ]]; then
  mv /dist/tyr_linux_arm64 /dist/tyr
else
  echo unexpected arch "${arch}"
  exit 1
fi
