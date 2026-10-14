param(
  [Parameter(Mandatory=$true)]
  [ValidateSet("disable-all","enable-all","stop-all","restart-all")]
  [string]$Action,

  [string]$Controller = "http://192.168.1.10:8443",
  [Parameter(Mandatory=$true)]
  [string]$Token
)

$headers = @{ Authorization = "Bearer $Token" }

Invoke-RestMethod -Method Post -Uri "$Controller/api/v1/admin/broadcast/$Action" -Headers $headers

Write-Host "Comando '$Action' enviado."
