![Go](https://github.com/flowerinthenight/oops/workflows/Go/badge.svg)

## Overview
`oops` is an automation-friendly, scalable, and scriptable API/generic testing tool designed to run on Kubernetes.

## Scenario file
```yaml
env:
  # These key/values will be added to the environment variables in your local
  # or in the pod oops will be running on.
  ENV_KEY1: value1
  ENV_KEY2: value2
  
# Any value that starts with '#!/' (i.e. #!/bin/bash) will be written to disk as
# an executable script file and the resulting output combined from stdout & stderr
# will become the final evaluated value. This is useful if you chain http calls,
# such as, the url of the second test is from the payload response of the first
# test, etc.
# If supported, the filename of the script will be indicated below.

# A list of http requests to perform sequentially. This tool will continue running
# all the list entries even if failure occurs during the execution.
run:
- http:
    method: POST
    
    # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_url
    # Example: /tmp/scenario01.yaml_run0_url
    url: "https://service.alphaus.cloud/users"
    
    # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_hdr.<key>
    # Example: /tmp/scenario01.yaml_run0_hdr.Authorization
    headers:
      Authorization: |
        #!/bin/bash
        echo -n "Bearer $TOKEN"
      Content-Type: application/json
      
    # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_qparams.<key>
    # Example: /tmp/scenario01.yaml_run0_qparams.key2
    query_params:
      key1: value1  
      key2: |
        #!/bin/bash
        echo -n "$KEY2"
        
    # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_payload
    # Example: /tmp/scenario01.yaml_run0_payload
    payload: |
      {"key1":"value1","key2":"value2"}
      
    # If response payload is not empty, its contents will be written in the
    # file below. Useful if you want to refer to it under 'asserts.shell'
    # and/or 'check'.
    response_out: /tmp/out.json
    
    asserts:
      # The expected http status code. Indicates a failure if not equal.
      status_code: 200
      
      # JSON validation using https://github.com/xeipuuv/gojsonschema package.
      validate_json: |
        {
          "type": "object",
          "properties": {
            ...
          }
        }
      
      # A non-zero return value indicates a failure.
      # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_assertshell
      # Example: /tmp/scenario01.yaml_run0_assertshell
      shell: |
        #!/bin/bash
        if [[ "$(cat /tmp/out.json | jq -r .username)" != "user01" ]]; then
          echo "try fail"
          exit 1
        fi

# A script to run after 'run', if present. Useful also as a standalone script
# in itself, if 'run' is empty. A non-zero return value indicates a failure.
check: |
  #!/bin/bash
  echo "check"
```
