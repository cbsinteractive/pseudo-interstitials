#!/bin/bash

if [ "$#" -ne 2 ]; then
    id="segment1"
    dest="https://2024.demuxed.com/#speakers"
else 
    id=$1
    dest=$2
fi

curl -i --header "Content-Type: application/json" \
  --request POST \
  --data "{\"id\":\"$id\",\"dest\":\"$dest\"}" \
  http://localhost:8082/add

