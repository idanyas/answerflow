#!/bin/bash

while true; do
    # shellcheck disable=SC2035
    export-repo -s 120k .
    export-repo .
    sleep 0.33s
done