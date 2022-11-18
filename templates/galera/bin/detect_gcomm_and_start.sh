#!/bin/bash

URI_FILE=/var/lib/mysql/gcomm_uri

rm -f /var/lib/mysql/mysql.sock
rm -f $URI_FILE

echo "Waiting for gcomm URI to be configured for this POD"
while [ ! -f $URI_FILE ]; do
      sleep 2
done
URI=$(cat $URI_FILE)
if [ "$URI" = "gcomm://" ]; then
   echo "this POD will now bootstrap a new galera cluster"
   sed -i -e 's/^\(safe_to_bootstrap\):.*/\1: 1/' /var/lib/mysql/grastate.dat
else
   echo "this POD will now join cluster $URI"
fi

rm -f $URI_FILE
exec /usr/libexec/mysqld --wsrep-cluster-address="$URI"
