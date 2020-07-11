## Scenario file
```yaml
env:
  # These key/values will be added to the environment variables in your local
  # or in the pod oops will be running on.
  ENV_KEY1: value1
  ENV_KEY2: value2
  
# Any value that starts with '#!/' (i.e. #!/bin/bash) will be written to disk as
# an executable script file and the resulting output combined from stdout & stderr
# will become the final evaluated value.
# If supported, the filename of the script will be indicated below.

# A list of http requests to perform sequentially.
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
    # Example: /tmp/scenario01.yaml_run0_qparams.key1
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
    response_out: /tmp/out.txt
    
    asserts:
      # The expected http status code. Indicates a failure if not equal.
      code: 200
      
      # A non-zero return value indicates a failure.
      # Filename: <tempdir>/<scenario-filename>.yaml_run<index>_assertshell
      # Example: /tmp/scenario01.yaml_run0_assertshell
      shell: |
        #!/bin/bash
        if [[ "$(cat /tmp/out.txt | jq -r .username)" != "user01" ]]; then
          echo "try fail"
          exit 1
        fi

# A script to run after 'run', if present. A non-zero return value indicates a failure.
check: |
  #!/bin/bash
  echo "check"
```
