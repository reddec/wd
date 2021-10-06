#!/bin/sh

if [ "$HEADER_X_ATTEMPT" != "2" ]; then
  #  fail always except second
  sleep "${QUERY_DELAY:-1}"
  echo 123
  exit 1
else
    echo "OK"
fi