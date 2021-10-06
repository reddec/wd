#!/bin/sh

echo "Hello ${QUERY_USER:-user}! I'm $USER"
echo "My ID: $(id)"
echo "My workdir: $(pwd)"