#!/usr/bin/env bash

arch=$(dpkg --print-architecture)

if [[ $arch == "amd64" ]]; then
  mv /dist/tyr-amd64 /dist/tyr
elif [[ $arch == "arm64" ]]; then
  mv /dist/tyr-arm64 /dist/tyr
else
  exit 1
fi
