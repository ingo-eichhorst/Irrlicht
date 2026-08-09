#!/bin/sh
# BASHISM: process substitution
while read -r l; do echo "$l"; done < <(ls)
