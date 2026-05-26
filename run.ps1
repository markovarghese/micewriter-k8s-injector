<#
.SYNOPSIS
  Build, push, and deploy the micewriter-k8s-injector.
.EXAMPLE
  .\run.ps1 push
  .\run.ps1 deploy
  .\run.ps1 undeploy
#>
param([Parameter(Mandatory)][string]$Target)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Path to the kubeconfig produced by the k3sonhyperv Ansible playbook.
# Update this if your k3sonhyperv repo is in a different location.
$kubeconfig = "D:\githubrepos\k3sonhyperv\kubeconfig"
if (-not (Test-Path $kubeconfig)) {
    Write-Error "kubeconfig not found at $kubeconfig — run install-k3s.yml first."
    exit 1
}

docker info > $null 2>&1
if ($LASTEXITCODE -ne 0) { Write-Error "Docker is not running."; exit 1 }

$registry  = "k8s-node-1.local:5000"
$image     = "micewriter-k8s-injector"
$tag       = "latest"
$fullTag   = "${registry}/${image}:${tag}"
$namespace = "micewriter-system"
$chartDir  = "charts/micewriter-k8s-injector"

function Invoke-Helm {
    docker run --rm -i `
        -v "${kubeconfig}:/kubeconfig:ro" `
        -e KUBECONFIG=/kubeconfig `
        -v "${PSScriptRoot}:/workspace:ro" `
        -w /workspace `
        alpine/helm:latest @args
}

switch ($Target) {
    "push" {
        Write-Host "Building $image..."
        docker build -t $fullTag .
        Write-Host "Pushing $fullTag..."
        docker push $fullTag
    }

    "deploy" {
        Write-Host "Deploying $image via Helm..."
        Invoke-Helm upgrade --install micewriter-k8s-injector $chartDir `
            --namespace $namespace --create-namespace `
            --wait
        Write-Host "Webhook deployed to namespace $namespace."
    }

    "undeploy" {
        Invoke-Helm uninstall micewriter-k8s-injector --namespace $namespace --ignore-not-found
    }

    default { Write-Error "Unknown target '$Target'. Use: push | deploy | undeploy" }
}
