#!/bin/sh
printf 'out-1'
sleep 0.02
printf 'err-1' >&2
sleep 0.02
printf 'out-2'
sleep 0.02
printf 'err-2' >&2
