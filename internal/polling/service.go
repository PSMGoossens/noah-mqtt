package polling

import (
	"encoding/json"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"log/slog"
	"noah-mqtt/internal/growatt"
	"noah-mqtt/internal/homeassistant"
	"os"
	"time"
)

type Options struct {
	GrowattClient   *growatt.Client
	HaClient        *homeassistant.Service
	MqttClient      mqtt.Client
	PollingInterval time.Duration
	TopicPrefix     string
}

type Service struct {
	options       Options
	serialNumbers []string
}

func NewService(options Options) *Service {
	return &Service{
		options: options,
	}
}

func (s *Service) Start() {
	if err := s.options.GrowattClient.Login(); err != nil {
		slog.Error("could not login to growatt account", slog.String("error", err.Error()))
		panic(err)
	}

	s.enumerateDevices()

	go s.poll()
}

func (s *Service) enumerateDevices() {
	serialNumbers := s.fetchNoahSerialNumbers()
	var devices []homeassistant.DeviceInfo
	for _, serialNumber := range serialNumbers {
		if data, err := s.options.GrowattClient.GetNoahInfo(serialNumber); err != nil {
			slog.Error("could not noah status", slog.String("error", err.Error()), slog.String("serialNumber", serialNumber))
		} else {
			batCount := len(data.Obj.Noah.BatSns)
			var batteries []homeassistant.BatteryInfo
			for i := 0; i < batCount; i++ {
				batteries = append(batteries, homeassistant.BatteryInfo{
					Alias:      fmt.Sprintf("BAT%d", i),
					StateTopic: s.stateTopicBattery(serialNumber, i),
				})
			}

			devices = append(devices, homeassistant.DeviceInfo{
				SerialNumber: serialNumber,
				Alias:        data.Obj.Noah.Alias,
				StateTopic:   s.deviceStateTopic(serialNumber),
				Batteries:    batteries,
				Model:        data.Obj.Noah.Model,
				Version:      data.Obj.Noah.Version,
			})
		}
	}

	s.serialNumbers = serialNumbers
	s.options.HaClient.SetDevices(devices)
}

func (s *Service) deviceStateTopic(serialNumber string) string {
	return fmt.Sprintf("%s/%s", s.options.TopicPrefix, serialNumber)
}

func (s *Service) stateTopicBattery(serialNumber string, index int) string {
	return fmt.Sprintf("%s/%s/BAT%d", s.options.TopicPrefix, serialNumber, index)
}

func (s *Service) fetchNoahSerialNumbers() []string {
	slog.Info("fetching plant list")
	list, err := s.options.GrowattClient.GetPlantList()
	if err != nil {
		slog.Error("could not get plant list", slog.String("error", err.Error()))
		panic(err)
	}

	var serialNumbers []string

	for _, plant := range list.Back.Data {
		slog.Info("fetch plant details", slog.String("plantId", plant.PlantID))
		if info, err := s.options.GrowattClient.GetNoahPlantInfo(plant.PlantID); err != nil {
			slog.Error("could not get plant info", slog.String("plantId", plant.PlantID), slog.String("error", err.Error()))
		} else {
			if len(info.Obj.DeviceSn) > 0 {
				serialNumbers = append(serialNumbers, info.Obj.DeviceSn)
				slog.Info("found device sn", slog.String("deviceSn", info.Obj.DeviceSn), slog.String("plantId", plant.PlantID), slog.String("topic", s.deviceStateTopic(info.Obj.DeviceSn)))
			}
		}
	}

	if len(serialNumbers) == 0 {
		slog.Info("no noah devices found")
		<-time.After(60 * time.Second)
		os.Exit(0)
	}

	return serialNumbers
}

func (s *Service) poll() {
	slog.Info("start polling growatt", slog.Int("interval", int(s.options.PollingInterval/time.Second)))
	for {
		for _, serialNumber := range s.serialNumbers {
			if data, err := s.options.GrowattClient.GetNoahStatus(serialNumber); err != nil {
				slog.Error("could not get device data", slog.String("error", err.Error()), slog.String("device", serialNumber))
			} else {
				if b, err := json.Marshal(noahStatusToPayload(data)); err != nil {
					slog.Error("could not marshal device data", slog.String("error", err.Error()), slog.String("device", serialNumber))
				} else {
					s.options.MqttClient.Publish(s.deviceStateTopic(serialNumber), 0, false, string(b))
					slog.Debug("device data received", slog.String("data", string(b)), slog.String("device", serialNumber))
				}
			}

			if data, err := s.options.GrowattClient.GetBatteryData(serialNumber); err != nil {
				slog.Error("could not get battery data", slog.String("error", err.Error()), slog.String("device", serialNumber))
			} else {
				for i, bat := range data.Obj.Batter {
					if b, err := json.Marshal(noahBatteryDetailsToBatteryPayload(&bat)); err != nil {
						slog.Error("could not marshal battery data", slog.String("error", err.Error()))
					} else {
						s.options.MqttClient.Publish(s.stateTopicBattery(serialNumber, i), 0, false, string(b))
						slog.Debug("battery data received", slog.String("data", string(b)), slog.String("device", serialNumber))
					}
				}
			}
		}

		<-time.After(s.options.PollingInterval)
	}
}
