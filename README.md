## Scenario file
```yaml
env:
  # These key/values will be added to the environment variables in your local
  # or in the pod oops will be running on.
  ENV_KEY1: value1
  ENV_KEY2: value2

run:
- http:
    method: GET
    url: "https://serviceqa.mobingi.com/m/u/me"
    headers:
      Authorization: |
        #!/bin/bash
        echo -n "Bearer $TOKEN"
    query_params:
      key1: value1  
      key2: |
        #!/bin/bash
        echo -n "$KEY2"
    payload: |
      {"key1":"value1","key2":"value2"}
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
