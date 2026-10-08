-- Decompiled with a typical Luau decompiler (sample input for BetterDecompiler)
local v1 = game:GetService("Players")
local v2 = game:GetService("ReplicatedStorage")
local v3 = game:GetService("TweenService")
local v4 = v1.LocalPlayer
local v5 = v4:WaitForChild("PlayerGui")
local v6 = v2:WaitForChild("Remotes"):WaitForChild("PurchaseItem")
local v7 = require(v2:WaitForChild("Modules"):WaitForChild("ShopConfig"))
local v8 = TweenInfo.new(0.25, Enum.EasingStyle.Quad, Enum.EasingDirection.Out)
local v9 = {}

local function u10(p1, p2)
	local v11 = v5:FindFirstChild("ShopGui")
	if not v11 then
		return
	end
	local v12 = v11:FindFirstChild("Frame")
	v3:Create(v12, v8, {
		Position = p2 and UDim2.new(0.5, 0, 0.5, 0) or UDim2.new(0.5, 0, 1.5, 0),
	}):Play()
	v9[p1] = p2
end

for v13, v14 in pairs(v7.Items) do
	local v15 = Instance.new("TextButton")
	v15.Name = v13
	v15.Text = `{v14.DisplayName} - {v14.Price}`
	v15.MouseButton1Click:Connect(function()
		v6:FireServer(v13)
	end)
end

v4.CharacterAdded:Connect(function(p1)
	local v16 = p1:WaitForChild("Humanoid")
	v16.Died:Connect(function()
		u10("Shop", false)
	end)
end)

game:GetService("UserInputService").InputBegan:Connect(function(p1, p2)
	if p2 then
		return
	end
	if p1.KeyCode == Enum.KeyCode.B then
		u10("Shop", not v9.Shop)
	end
end)
