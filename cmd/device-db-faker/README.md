# Device DB Faker
This simple command line tool updates device records in the database directly so that developers can more easily test user-facing device APIs.

It currently supports a single command:

`device-db-faker updatestatus -f FILENAME -l LABELSELECTOR`

The specified file should contain a device record in YAML or JSON format.  The command will take the status of that device record and apply it to all devices that match the specified label selector.
