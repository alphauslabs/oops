## Scenario file
```yaml
env:
  # These key/values will be added to the environment variables in your local
  # or in the pod oops will be running on.
  ENV_KEY1: value1
  ENV_KEY2: value2

run:
# A list of http requests to perform sequentially.
- http:
    method: POST
    url: "https://service.alphaus.cloud/users"
    headers:
      Authorization: |
        #!/bin/bash
        echo -n "Bearer $TOKEN"
      Content-Type: application/json
    query_params:
      key1: value1  
      key2: |
        #!/bin/bash
        echo -n "$KEY2"
    payload: |
      {"key1":"value1","key2":"value2"}
    # If response payload is not empty, its contents will be written in the
    # file below. Useful if you want to refer to it under 'asserts.shell'
    # and/or 'check'.
    response_out: /tmp/out.txt
    asserts:
      code: 200
      shell: |
        #!/bin/bash
        if [[ "$(cat /tmp/out.txt | jq -r .username)" == "user01" ]]; then
          echo "try fail"
          exit 1
        fi

check: |
  #!/bin/bash
  echo "check"
```
