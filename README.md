# mygograph

**`mygograph`** 是基于 [`github.com/egoist/mygo`](https://github.com/egoist/mygo) 构建的 **100% 纯 Go（`CGO_ENABLED=0`）高性能科学计算与工程实时可视化图表库**（Go 生态对标 Python `PyQtGraph` 的原生级方案）。

专为数字示波器、64~256 导联高密度脑电/肌电监护（EEG/iEEG/EMG）、软件无线电（SDR）频谱瀑布图、工业振动监测与光学干涉仪剖析等严苛场景设计，兼顾 **微秒级极值保真度**、**稳态渲染热路径 `0 allocs/op`** 与 **GPU Kage 着色器硬件加速**。

---

## 核心特性

- **零精度损失双精度相机相对投影（`ViewBox`）**
  - CPU 侧先在 `float64` 空间完成相对于视口起点的局部差值与缩放，最后一步再投影至 `float32` 屏幕 DIP 坐标，彻底消除 Unix 秒级时间戳（`1.77e9`）放大至微秒/纳秒级窗口时的 `float32` 阶梯抖动。
  - 完整复刻 `PyQtGraph` 经典工程手势：左键拖拽平移（或 `RectMode` 橡皮筋框选放大 + 历史栈回退）、右键按住水平/垂直独立非等比锚点缩放、滚轮光标锚点缩放（`Ctrl+滚轮` 仅缩 X、`Shift+滚轮` 仅缩 Y）、双击一键 `AutoRange`、多子图 `LinkX` / `LinkY` 防重入双向同步。
- **零分配降采样内核与亿级样本多级金字塔（`data`）**
  - **像素对齐 `M4-MinMax` 四元组包络算法**：单通道 `1,000,000` 点降采样至 `2000` 像素桶仅需 **`~0.17 ms`**（`0 B/op, 0 allocs/op`），100% 保留单样本狄拉克尖峰毛刺（Dirac Glitch）与 `NaN` 断线空洞。
  - **分支因子 `B=64` 多级极值金字塔（`MinMaxPyramid`）**：`100,000,000` 点（1 亿点）超长历史记录任意倍率平滑滚轮缩放查询耗时 **`0.11 ~ 0.22 ms`**（`0 B/op, 0 allocs/op`），内存开销仅原始序列的 `~3.2%`。
  - **视觉趋势精选 `LTTB`** 与并发安全无分配 **`RingBuffer` / `MultiRingBuffer`**。
- **GPU Kage 着色器双快路径（`shaders` & `colormap`）**
  - **`lut16.kage` 16-bit 动态伪彩与单 DrawCall 环形瀑布图**：将标量矩阵高 8 位/低 8 位打包至 `R/G` 通道（`65536` 级量化），配合 `Viridis`、`Plasma`、`Inferno`、`Magma`、`Cividis`、`Turbo`、`Coolwarm`、`Grayscale` 色表，拖动 `HistogramLUT` 调节窗宽窗位时 **CPU 像素重算 `0 ms`**（纯 Uniform 更新）；`WaterfallItem` 通过 `fract(uv.y + RowOffset)` 环形寻址实现零内存整图搬移滚动。
  - **`multistack.kage` 256 导联并行实时波形引擎**：`<= 32` 导联走矢量快路径，`> 32` 导联自动切换至单次 DrawCall 紧凑极值数据纹理 + 片段着色器亚像素抗锯齿管线；配合 L3 缓存常驻保序 `int32` 镜像、`B=8` 分块预聚合与 4 路常驻零分配并行打包工作池，**`256` 导联 × `1000 Hz` × `10 s` 视口单帧仅需 `~1.62 ms`（`0 allocs/op`）**，原生支持滚动走纸（Roll）与医疗监护仪擦除扫描条（Sweep / Erase-Bar）双模式、硬件级实时去直流偏置（DC Removal）、坏道灰化与双击单导联独奏放大（Solo）。
- **完备工程测量、ROI 切片与导出工具链（`items` & `export.go`）**
  - **工程坐标轴与多图对齐**：`LinearScale` / `LogScale` / `TimeScale` 自动 SI 国际单位词头（`f, p, n, μ, m, k, M, G, T`），`ui.Shape` 字形缓存 + 启用 `"tnum"` 等宽表格数字防抖动；`GraphLayout` 自动同步上下多子图左侧 Y 轴像素边距（误差 `0 px`）。
  - **游标与交互剖析**：`InfiniteLine`（垂直/水平/任意倾角参考线）、`CursorMeasureItem`（实时读取 `Δt`、`1/Δt`、`ΔY`、`Vpp`、`RMS`）、`Crosshair`（自动就近吸附真实采样点）、`LinearRegion`（概览-详情双向联动截取）、`RectROI`（8 手柄二维子矩阵提取）、`LineROI`（双线性插值剖面线实时提取）、`ScatterItem`（6 种几何标记 + 屏幕像素去重 + `512×512` CSR 空间网格 `< 1 μs` 百万点悬停拾取）。
  - **按需休眠与高清导出**：数据流暂停且无交互时自动停止请求 `AnimationFrame`（**`0%` 空闲 CPU 占用**）；内置右键菜单与编程 API 一键导出 `2×`/`3×` HiDPI 出版级 PNG 及当前视口全精度 CSV。

---

## 环境要求与安装

- **Go 版本**：`go 1.27+`
- **唯一直接依赖**：`github.com/egoist/mygo v0.9.0`（全程 `CGO_ENABLED=0` 纯 Go 编译，零 C/C++ 工具链依赖）

```bash
go get github.com/gaoxi/mygograph
```

---

## 快速上手（30 行构建百万点示波器）

```go
package main

import (
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func main() {
	// 1. 生成 1,000,000 点 1 MHz 采样波形（含单样本尖峰毛刺）
	const n, fs = 1_000_000, 1_000_000.0
	raw := make([]float64, n)
	for i := range n {
		t := float64(i) / fs
		raw[i] = math.Sin(2*math.Pi*1200*t) + 0.25*math.Cos(2*math.Pi*9500*t)
	}
	raw[n/2] = 4.5 // 单样本纳秒级毛刺：M4 降采样 100% 保留

	src := data.NewUniformSlice(0, fs, raw)
	th := mygograph.DarkTheme()

	// 2. 创建带 SI 单位自动换算坐标轴的 Plot 画布并挂载曲线、双游标与十字准星
	plot := mygograph.NewPlot(
		mygograph.WithTitle("1M Points Oscilloscope"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Time")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Voltage").SetUnit("V")),
	)
	curve := items.NewCurve(src, items.WithName("CH1"), items.WithColor(th.SeriesColor(0)))
	cursors := items.NewCursorMeasure(0.45, 0.55, items.WithMeasureSource(src))
	plot.AddItem(curve, cursors, items.NewCrosshair(items.WithSnapTarget(curve)))

	// 3. 启动原生桌面窗口
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:   "mygograph Quick Start",
			Width:   1200,
			Height:  720,
			Content: ui.View(func(c *ui.Context) { plot.View(c).Fill() }),
		})
	})
	_ = mygo.App.Run()
}
```

---

## 四大旗舰示例程序（`examples/`）

所有示例均支持直接启动交互式桌面窗口，或附加 `-export <path.png>` 参数在无头环境下离屏导出 `2×` HiDPI 高清快照：

| 示例目录 | 典型工业/科研场景 | 运行命令 |
| :--- | :--- | :--- |
| [`examples/01_million_points_cursor`](./examples/01_million_points_cursor/main.go) | 千万~亿级采样点超深存储示波器（`MinMaxPyramid` 保峰缩放 + `T1/T2` 游标 `Δt/f/Vpp/RMS` 测量 + 亮暗主题切换） | `CGO_ENABLED=0 go run ./examples/01_million_points_cursor` |
| [`examples/02_eeg_256ch_realtime`](./examples/02_eeg_256ch_realtime/main.go) | `256` 导联 × `1000 Hz` 高密度脑电实时监护仪（GPU `multistack.kage` + 擦除扫描/滚动走纸切换 + 实时去直流 + 双击独奏放大） | `CGO_ENABLED=0 go run ./examples/02_eeg_256ch_realtime` |
| [`examples/03_spectrogram_waterfall`](./examples/03_spectrogram_waterfall/main.go) | 纯 Go 实时 STFT 时频谱分析仪（上下双图 `0 px` 轴对齐 + 单 DrawCall 环形滚动瀑布图 + `HistogramLUT` `0 ms` 调窗） | `CGO_ENABLED=0 go run ./examples/03_spectrogram_waterfall` |
| [`examples/04_linked_region_roi`](./examples/04_linked_region_roi/main.go) | 一维概览-详情 `LinearRegion` 双向联动 + 二维干涉光斑热力图 `RectROI` 能量统计与 `LineROI` 实时双线性横截面剖析台 | `CGO_ENABLED=0 go run ./examples/04_linked_region_roi` |

---

## 仓库目录结构

```text
mygograph/
├── theme.go               # DarkTheme / LightTheme 高对比度不透明调色板与工程视觉规范
├── scale.go               # LinearScale / LogScale / TimeScale 与 SI 国际单位词头自动格式化
├── viewbox.go             # float64 相机相对坐标变换、缩放历史栈、范围约束与 LinkX/LinkY 双向同步
├── axis.go                # 坐标轴组件（ui.Shape 字形缓存 + OpenType "tnum" 等宽数字防抖）
├── item.go                # Item / OverlayItem / InteractiveItem / SnapTarget / StreamItem 核心协议
├── plot.go                # Plot 画布手势状态机、网格纹理缓存、图例面板与按需重绘节流引擎
├── layout.go              # GraphLayout 多子图网格与跨行左侧 Y 轴像素级边距对齐
├── export.go              # 2x/3x 倍采样离屏 PNG 导出与当前视口全精度 CSV 导出
├── data/
│   ├── source.go          # XYSource 接口、XYSlice、UniformSlice 与二分检索工具
│   ├── ring.go            # 并发安全零分配 RingBuffer 与 L3 常驻保序 int32 加速 MultiRingBuffer
│   ├── decimate.go        # 像素桶对齐 M4-MinMax 保峰算法与 LTTB 视觉降采样器
│   └── pyramid.go         # 分支因子 B=64 的亿级样本多级极值金字塔 MinMaxPyramid
├── colormap/
│   ├── lut.go             # Viridis / Plasma / Inferno / Turbo / Coolwarm 等 256×1 GPU LUT
│   └── colorbar.go        # ColorControl 响应式控制器、ColorBar 与交互式 HistogramLUT 直方图调窗器
├── shaders/
│   ├── lut16.kage         # 16-bit 双通道标量解包、环形 UV 瀑布寻址与解包后双线性插值着色器
│   ├── multistack.kage    # 64~256 导联单 DrawCall 并行抗锯齿竖条包络与扫描擦除着色器
│   └── shaders.go         # go:embed 着色器编译缓存与零分配 Uniform 状态封装
├── items/
│   ├── curve.go           # CurveItem 高通量折线/阶梯线/基线填充与亚毫秒级就近吸附
│   ├── cursor.go          # InfiniteLine 参考线、CursorMeasureItem 游标统计与 Crosshair 十字准星
│   ├── image.go           # ImageItem 16-bit GPU 热力图与 WaterfallItem 环形滚动瀑布图
│   ├── multichannel.go    # MultiChannelItem 双模（矢量 / GPU Shader）高密度多导联实时流引擎
│   ├── roi.go             # LinearRegion 联动选区、RectROI 8 手柄矩形框与 LineROI 剖面采样器
│   └── scatter.go         # ScatterItem 多形状标记、像素位图去重与 512×512 CSR 空间网格拾取
└── examples/              # 四大核心科研/工业场景完整可运行示例
```

---

## 实测性能门禁报告（Benchmark Gates）

测试平台：`AMD Ryzen 7 8845HS (16 Threads)`，`Windows amd64`，`Go 1.27.3`，`CGO_ENABLED=0`：

| 门禁项 | 测试规格 | 设计门槛 (`DESIGN.md` §9.1) | 实测指标 | 结果 |
| :--- | :--- | :--- | :--- | :---: |
| **Gate 1：降采样内核吞吐** | `1,000,000` 点 `M4-MinMax` 投影至 `2000` 像素桶 | `< 0.8 ms`, `0 allocs/op` | **`0.181 ms/op`**, `0 B/op`, `0 allocs/op` | PASS |
| **Gate 2：亿级金字塔缩放** | `100,000,000` 点（1 亿点）全跨度~微秒级缩放查询 | `< 0.3 ms`, `0 allocs/op` | **`0.054 ~ 0.126 ms/op`**, `0 B/op`, `0 allocs/op` | PASS |
| **Gate 3：256 导联实时脑电** | `256` 通道 × `1000 Hz` × `10 s` 视口 GPU 管线帧提交 | `< 6.0 ms`, `0 allocs/op` | **`1.62 ms/op`**, `0 B/op`, `0 allocs/op` | PASS |
| **Gate 4：热力图调窗与瀑布推流** | `1024 × 1024` `HistogramLUT` 调窗 / `1024` 列 `PushRow` | `0 ms` 重算, `0 allocs/op` | **`19.5 ns/op`** 调窗 / **`4.83 μs/op`** 推流, `0 allocs/op` | PASS |
| **Gate 5：流暂停空闲功耗** | 实时流停止写入且无指针交互后 | 自动停止 `AnimationFrame` | 稳态重绘帧计数严格归 **`0`**（`0%` 空闲 CPU） | PASS |
| **附加：百万散点悬停拾取** | `1,000,000` 点 `ScatterItem.SnapNearest` 空间网格查询 | `< 0.1 ms`, `0 allocs/op` | **`844 ns/op` (`0.00084 ms`)**, `0 B/op`, `0 allocs/op` | PASS |

运行全量单元测试与基准测试：

```bash
CGO_ENABLED=0 go test -count=1 ./...
CGO_ENABLED=0 go test -run=^$ -bench=. -benchmem ./data ./colormap ./items
```

---

## 架构设计文档

完整系统分层、坐标变换数学推导、Kage 着色器内存布局以及实现阶段的工程优化细节请参阅 [`DESIGN.md`](./DESIGN.md)。
