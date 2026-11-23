locals {
  compute_actions = [
    "Microsoft.App/containerApps/*",
    # Migration runner job (#1966): create/update and start executions.
    # Needs a human re-apply of this bootstrap stack before the first deploy
    # that creates the job; the deploy SP must never get this by other means.
    "Microsoft.App/jobs/*",
    "Microsoft.App/managedEnvironments/*",
    "Microsoft.App/locations/*",
    "Microsoft.ContainerRegistry/registries/*",
    "Microsoft.ContainerRegistry/registries/tasks/*",
    "Microsoft.ContainerService/managedClusters/*",
    "Microsoft.Logic/workflows/*",
    "Microsoft.Logic/locations/*",
    "Microsoft.Cdn/profiles/*",
    "Microsoft.Cdn/operationresults/*",
    "Microsoft.Cdn/checkNameAvailability/*",
    "Microsoft.ManagedIdentity/userAssignedIdentities/*",
    "Microsoft.ManagedIdentity/operations/*",
  ]
}
