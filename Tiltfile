load('ext://restart_process', 'docker_build_with_restart')

local_resource('Install CAPI dependencies',
               'make apply-capi-dependencies'
)

local_resource('Wait CAPI dependencies resources',
               'make wait-capi-dependencies-resources',
               resource_deps=[
                 'Install CAPI dependencies'
               ]
)

local_resource('Install Cluster API',
               'make apply-capi'
)

local_resource('Wait CAPI resources',
               'make wait-capi-resources',
               resource_deps=[
                 'Install Cluster API'
               ]
)

local_resource('Install CRDs',
               'make install',
)

local_resource('Build manager binary',
               'make build',
)

docker_build_with_restart('manager:test',
             '.',
             dockerfile='./Dockerfile.dev',
             entrypoint='/manager',
             live_update=[
               sync('./bin/manager', '/manager')
             ]
)

k8s_yaml('.kubernetes/dev/manifest.yaml')