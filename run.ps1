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

# Path to the kubeconfig
if ($env:KUBECONFIG) {
    $kubeconfig = $env:KUBECONFIG
} else {
    $kubeconfig = "$HOME\.kube\config"
}
if (-not (Test-Path $kubeconfig)) {
    Write-Error "kubeconfig not found at $kubeconfig"
    exit 1
}

# docker info > $null 2>&1
# if ($LASTEXITCODE -ne 0) { Write-Error "Docker is not running."; exit 1 }

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
        docker build -t "localhost:5000/${image}:${tag}" .
        
        Write-Host "Starting temporary port-forward to registry..."
        docker run --rm -d --name registry-pf -p 5000:5000 -v "${kubeconfig}:/kubeconfig:ro" -e KUBECONFIG=/kubeconfig bitnami/kubectl:latest port-forward -n micewriter-infra svc/registry 5000:5000 --address 0.0.0.0
        Start-Sleep -Seconds 5
        
        try {
            Write-Host "Pushing to localhost:5000/${image}:${tag}..."
            docker push "localhost:5000/${image}:${tag}"
        } finally {
            Write-Host "Stopping port-forward..."
            docker rm -f registry-pf
        }
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
