#!/usr/bin/env bash

arch=dpkg --print-architecture

if [[ $arch == "amd64" ]]; then
  mv /usr/local/bin/tyr-amd64 /usr/local/bin/tyr
elif [[ $arch == "arm64" ]]; then
  mv /usr/local/bin/tyr-arm64 /usr/local/bin/tyr
else
  exit 1
fi
